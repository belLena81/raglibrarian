package artifact

import (
	"context"
	"errors"
	"testing"
	"time"
)

type cleanerRepository struct {
	deletions []DeletionArtifact
	completed []string
	retried   []string
}

func (r *cleanerRepository) ClaimOrphans(context.Context, time.Time, time.Time, time.Duration, int) ([]Orphan, error) {
	return nil, nil
}

func (r *cleanerRepository) CompleteOrphanCleanup(context.Context, string, time.Time) error {
	return nil
}

func (r *cleanerRepository) RetryOrphanCleanup(context.Context, string, time.Time) error {
	return nil
}

func (r *cleanerRepository) ClaimDeletionArtifacts(context.Context, time.Time, time.Duration, int) ([]DeletionArtifact, error) {
	return r.deletions, nil
}

func (r *cleanerRepository) CompleteDeletionArtifact(_ context.Context, eventID, jobID string, _ time.Time) error {
	r.completed = append(r.completed, eventID+":"+jobID)
	return nil
}

func (r *cleanerRepository) RetryDeletionArtifact(_ context.Context, jobID string, _ time.Time) error {
	r.retried = append(r.retried, jobID)
	return nil
}

type deletionStore struct {
	prefixes []string
	err      error
}

func (s *deletionStore) DeletePrefix(_ context.Context, prefix string) error {
	s.prefixes = append(s.prefixes, prefix)
	return s.err
}

func TestCleanerCompletesOnlyAfterExactPrefixDeletion(t *testing.T) {
	repository := &cleanerRepository{deletions: []DeletionArtifact{{
		EventID: "delete-event",
		JobID:   "job-1",
		Prefix:  "books/book-1/source-digest/profile-digest/",
	}}}
	store := &deletionStore{}
	cleaner, err := NewCleaner(repository, store, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = cleaner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.prefixes) != 1 || store.prefixes[0] != repository.deletions[0].Prefix {
		t.Fatalf("deleted prefixes = %#v", store.prefixes)
	}
	if len(repository.completed) != 1 || len(repository.retried) != 0 {
		t.Fatalf("completed=%#v retried=%#v", repository.completed, repository.retried)
	}
}

func TestCleanerRetriesPartialDeletionWithoutAcknowledging(t *testing.T) {
	repository := &cleanerRepository{deletions: []DeletionArtifact{{
		EventID: "delete-event",
		JobID:   "job-1",
		Prefix:  "books/book-1/source-digest/profile-digest/",
	}}}
	store := &deletionStore{err: errors.New("storage unavailable")}
	cleaner, err := NewCleaner(repository, store, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = cleaner.RunOnce(context.Background()); err == nil {
		t.Fatal("expected cleanup error")
	}
	if len(repository.completed) != 0 || len(repository.retried) != 1 {
		t.Fatalf("completed=%#v retried=%#v", repository.completed, repository.retried)
	}
}
