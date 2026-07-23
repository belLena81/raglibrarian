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

func TestReconcilerContinuesAfterPendingOriginalDeleteFailure(t *testing.T) {
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	failing := "originals/AAAAAAAAAAAAAAAAAAAAAA.pdf"
	pending := "originals/BBBBBBBBBBBBBBBBBBBBBB.pdf"
	orphan := "originals/CCCCCCCCCCCCCCCCCCCCCC.pdf"
	store := &reconcileStoreFake{
		objects: []StoredObject{{Reference: orphan, LastModified: now.Add(-2 * time.Hour)}},
		deleteErrors: map[string]error{
			failing: errors.New("object locked"),
		},
	}
	repository := &reconcileRepositoryFake{
		pending: []PendingOriginalDeletion{
			{BookID: "book-failing", CommandID: "command-failing", LifecycleVersion: 2, ObjectReference: failing},
			{BookID: "book-pending", CommandID: "command-pending", LifecycleVersion: 2, ObjectReference: pending},
		},
	}
	recorder := &reconcileRecorderFake{}
	reconciler := NewReconciler(repository, store, time.Hour, recorder)
	reconciler.now = func() time.Time { return now }

	result, err := reconciler.RunPass(context.Background(), "")

	if err == nil {
		t.Fatal("expected pending deletion failure")
	}
	if len(repository.marked) != 1 || repository.marked[0] != "book-pending" {
		t.Fatalf("marked original deletions = %#v", repository.marked)
	}
	if len(store.deleted) != 2 || store.deleted[0] != pending || store.deleted[1] != orphan {
		t.Fatalf("deleted = %#v", store.deleted)
	}
	if result.Scanned != 1 || result.Deleted != 2 {
		t.Fatalf("result = %+v", result)
	}
	if recorder.failed != 1 || recorder.scanned != 0 || recorder.deleted != 0 {
		t.Fatalf("recorder = %+v", recorder)
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
	objects      []StoredObject
	deleted      []string
	deleteErrors map[string]error
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
	if s.deleteErrors != nil {
		if err := s.deleteErrors[reference]; err != nil {
			return err
		}
	}
	s.deleted = append(s.deleted, reference)
	return nil
}

type reconcileRepositoryFake struct {
	referenced map[string]bool
	pending    []PendingOriginalDeletion
	marked     []string
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

func (r *reconcileRepositoryFake) PendingOriginalDeletions(context.Context, int) ([]PendingOriginalDeletion, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]PendingOriginalDeletion(nil), r.pending...), nil
}

func (r *reconcileRepositoryFake) MarkOriginalDeleted(_ context.Context, bookID, _ string, _ int64, _ time.Time) (Book, error) {
	if r.err != nil {
		return Book{}, r.err
	}
	r.marked = append(r.marked, bookID)
	return Book{ID: bookID, OriginalDeleted: true}, nil
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
