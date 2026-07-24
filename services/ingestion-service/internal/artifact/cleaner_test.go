package artifact

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type cleanerRepository struct {
	mu             sync.Mutex
	deletions      []DeletionArtifact
	completed      []string
	retried        []string
	orphanLease    time.Duration
	deletionLease  time.Duration
	deletionClaims chan struct{}
	completedCh    chan struct{}
}

func (r *cleanerRepository) ClaimOrphans(_ context.Context, _ time.Time, _ time.Time, lease time.Duration, _ int) ([]Orphan, error) {
	r.orphanLease = lease
	return nil, nil
}

func (r *cleanerRepository) CompleteOrphanCleanup(context.Context, string, time.Time) error {
	return nil
}

func (r *cleanerRepository) RetryOrphanCleanup(context.Context, string, time.Time) error {
	return nil
}

func (r *cleanerRepository) ClaimDeletionArtifacts(_ context.Context, _ time.Time, lease time.Duration, _ int) ([]DeletionArtifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletionLease = lease
	if r.deletionClaims != nil {
		select {
		case r.deletionClaims <- struct{}{}:
		default:
		}
	}
	return append([]DeletionArtifact(nil), r.deletions...), nil
}

func (r *cleanerRepository) CompleteDeletionArtifact(_ context.Context, eventID, jobID string, _ time.Time) error {
	r.completed = append(r.completed, eventID+":"+jobID)
	if r.completedCh != nil {
		select {
		case r.completedCh <- struct{}{}:
		default:
		}
	}
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

func TestCleanerUsesConfiguredIntervalForClaimLeases(t *testing.T) {
	repository := &cleanerRepository{}
	interval := 7 * time.Minute
	cleaner, err := NewCleaner(repository, &deletionStore{}, interval, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = cleaner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.orphanLease != interval {
		t.Fatalf("orphan claim lease = %s, want %s", repository.orphanLease, interval)
	}
	if repository.deletionLease != interval {
		t.Fatalf("deletion claim lease = %s, want %s", repository.deletionLease, interval)
	}
}

func TestCleanerWakesImmediatelyForAcceptedDeletion(t *testing.T) {
	repository := &cleanerRepository{
		deletionClaims: make(chan struct{}, 2),
		completedCh:    make(chan struct{}, 1),
	}
	store := &deletionStore{}
	cleaner, err := NewCleaner(repository, store, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cleaner.Run(ctx) }()
	select {
	case <-repository.deletionClaims:
	case <-time.After(time.Second):
		t.Fatal("cleaner did not complete its initial pass")
	}
	repository.mu.Lock()
	repository.deletions = []DeletionArtifact{{EventID: "delete-event", JobID: "job-1", Prefix: "books/book-1/"}}
	repository.mu.Unlock()
	cleaner.WakeDeletionCleanup()
	select {
	case <-repository.deletionClaims:
	case <-time.After(time.Second):
		t.Fatal("accepted deletion waited for the cleanup ticker")
	}
	select {
	case <-repository.completedCh:
	case <-time.After(time.Second):
		t.Fatal("accepted deletion was not completed after wake")
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
	if len(store.prefixes) != 1 || store.prefixes[0] != "books/book-1/" {
		t.Fatalf("deleted prefixes = %#v", store.prefixes)
	}
}
