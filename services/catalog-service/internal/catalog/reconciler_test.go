package catalog

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestReconcilerDeletesOnlyOldUnreferencedGeneratedObjects(t *testing.T) {
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	recent := now.Add(-30 * time.Minute)
	referenced := "originals/AAAAAAAAAAAAAAAAAAAAAA.pdf"
	orphan := "originals/BBBBBBBBBBBBBBBBBBBBBB.pdf"
	store := &reconcileStoreFake{objects: []StoredObject{
		{Reference: referenced, LastModified: old},
		{Reference: orphan, LastModified: old},
		{Reference: "originals/CCCCCCCCCCCCCCCCCCCCCC.pdf", LastModified: recent},
		{Reference: "originals/not-generated.pdf", LastModified: old},
		{Reference: "derived/DDDDDDDDDDDDDDDDDDDDDD.pdf", LastModified: old},
	}}
	repository := &reconcileRepositoryFake{referenced: map[string]bool{referenced: true}}
	recorder := &reconcileRecorderFake{}
	reconciler := NewReconciler(repository, store, time.Hour, recorder)
	reconciler.now = func() time.Time { return now }

	result, err := reconciler.RunPass(context.Background(), "")
	if err != nil {
		t.Fatalf("RunPass(): %v", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != orphan {
		t.Fatalf("deleted = %v", store.deleted)
	}
	if result.Scanned != 5 || result.Deleted != 1 {
		t.Fatalf("result = %+v", result)
	}
	if recorder.scanned != 5 || recorder.deleted != 1 || recorder.failed != 0 {
		t.Fatalf("recorder = %+v", recorder)
	}
}

func TestReconcilerDatabaseFailureDeletesNothing(t *testing.T) {
	store := &reconcileStoreFake{objects: []StoredObject{{
		Reference:    "originals/AAAAAAAAAAAAAAAAAAAAAA.pdf",
		LastModified: time.Now().Add(-2 * time.Hour),
	}}}
	recorder := &reconcileRecorderFake{}
	reconciler := NewReconciler(&reconcileRepositoryFake{err: errors.New("database unavailable")}, store, time.Hour, recorder)

	if _, err := reconciler.RunPass(context.Background(), ""); err == nil {
		t.Fatal("expected repository failure")
	}
	if len(store.deleted) != 0 {
		t.Fatalf("deleted = %v", store.deleted)
	}
	if recorder.failed != 1 {
		t.Fatalf("failures = %d", recorder.failed)
	}
}

func TestReconcilerBoundsOnePass(t *testing.T) {
	objects := make([]StoredObject, maximumReconcileObjects+100)
	for index := range objects {
		objects[index] = StoredObject{
			Reference:    fmt.Sprintf("originals/%022d.pdf", index),
			LastModified: time.Now().Add(-2 * time.Hour),
		}
	}
	store := &reconcileStoreFake{objects: objects}
	reconciler := NewReconciler(&reconcileRepositoryFake{}, store, time.Hour, &reconcileRecorderFake{})

	result, err := reconciler.RunPass(context.Background(), "")
	if err != nil {
		t.Fatalf("RunPass(): %v", err)
	}
	if result.Scanned != maximumReconcileObjects || len(store.deleted) != maximumReconcileObjects {
		t.Fatalf("scanned=%d deleted=%d", result.Scanned, len(store.deleted))
	}
	if result.NextCursor == "" {
		t.Fatal("expected continuation cursor")
	}
}

type reconcileStoreFake struct {
	objects []StoredObject
	deleted []string
}

func (s *reconcileStoreFake) ListCompleted(_ context.Context, _ string, cursor string, limit int) ([]StoredObject, string, error) {
	start := 0
	if cursor != "" {
		for start < len(s.objects) && s.objects[start].Reference != cursor {
			start++
		}
		if start < len(s.objects) {
			start++
		}
	}
	end := min(start+limit, len(s.objects))
	next := ""
	if end < len(s.objects) && end > start {
		next = s.objects[end-1].Reference
	}
	return append([]StoredObject(nil), s.objects[start:end]...), next, nil
}

func (s *reconcileStoreFake) Delete(_ context.Context, reference string) error {
	s.deleted = append(s.deleted, reference)
	return nil
}

type reconcileRepositoryFake struct {
	referenced map[string]bool
	err        error
}

func (r *reconcileRepositoryFake) ReferencesExist(_ context.Context, references []string) (map[string]bool, error) {
	if r.err != nil {
		return nil, r.err
	}
	result := make(map[string]bool, len(references))
	for _, reference := range references {
		result[reference] = r.referenced[reference]
	}
	return result, nil
}

type reconcileRecorderFake struct {
	scanned int
	deleted int
	failed  int
}

func (r *reconcileRecorderFake) ReconciliationCompleted(scanned, deleted int) {
	r.scanned += scanned
	r.deleted += deleted
}

func (r *reconcileRecorderFake) ReconciliationFailed() { r.failed++ }
