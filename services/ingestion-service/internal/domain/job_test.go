package domain

import (
	"errors"
	"testing"
	"time"
)

func TestProcessingJobLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	job, err := NewProcessingJob("job-1", "book-1", checksum(1), "config-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = job.Claim("worker-1", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if job.State() != JobProcessing || job.Attempts() != 1 {
		t.Fatalf("unexpected claim state: %s attempts=%d", job.State(), job.Attempts())
	}
	if err = job.Complete("worker-1", "manifest.pb", checksum(2), 42, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = job.Fail("worker-1", FailureMalformedDocument, now.Add(2*time.Second)); !errors.Is(err, ErrTerminalJob) {
		t.Fatalf("expected terminal error, got %v", err)
	}
}

func TestProcessingJobRejectsWrongLeaseOwner(t *testing.T) {
	now := time.Now().UTC()
	job, _ := NewProcessingJob("job-1", "book-1", checksum(1), "config-1", now)
	_ = job.Claim("worker-1", now, time.Minute)
	if err := job.RenewLease("worker-2", now.Add(time.Second), time.Minute); !errors.Is(err, ErrLeaseNotOwned) {
		t.Fatalf("expected lease ownership error, got %v", err)
	}
}

func TestProcessingJobLeaseExpiresAtExactBoundary(t *testing.T) {
	now := time.Now().UTC()
	job, _ := NewProcessingJob("job-1", "book-1", checksum(1), "config-1", now)
	_ = job.Claim("worker-1", now, time.Minute)
	if err := job.RenewLease("worker-1", now.Add(time.Minute), time.Minute); !errors.Is(err, ErrLeaseNotOwned) {
		t.Fatalf("expected exact expiry to reject renewal, got %v", err)
	}
	if err := job.Complete("worker-1", "manifest.pb", checksum(2), 42, now.Add(time.Minute)); !errors.Is(err, ErrLeaseNotOwned) {
		t.Fatalf("expected exact expiry to invalidate owner, got %v", err)
	}
	if err := job.Claim("worker-2", now.Add(time.Minute), time.Minute); err != nil {
		t.Fatalf("expected exact expiry to permit a new claim, got %v", err)
	}
}

func TestRestoreProcessingJobRejectsInconsistentState(t *testing.T) {
	now := time.Now().UTC()
	_, err := RestoreProcessingJob("job-1", "book-1", checksum(1), "config-1", JobProcessing, 1, "", time.Time{}, time.Time{}, "", "", [32]byte{}, 0, now, now)
	if !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("expected invalid restored state, got %v", err)
	}
}

func checksum(value byte) [32]byte {
	var sum [32]byte
	sum[0] = value
	return sum
}
