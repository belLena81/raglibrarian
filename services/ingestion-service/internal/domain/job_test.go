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

func TestProcessingJobWaitsForAndResumesAfterContentSelection(t *testing.T) {
	now := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	job, err := NewProcessingJob("job-1", "book-1", checksum(1), "config-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = job.Claim("ingestion-1", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = job.AwaitContentSelection("ingestion-1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if job.State() != JobAwaitingSelection || job.LeaseOwner() != "" || !job.LeaseExpiresAt().IsZero() {
		t.Fatalf("unexpected waiting state: %s owner=%q expiry=%v", job.State(), job.LeaseOwner(), job.LeaseExpiresAt())
	}
	if err = job.ResumeAfterContentSelection("ingestion-2", now.Add(2*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if job.State() != JobProcessing || job.Attempts() != 1 || job.LeaseOwner() != "ingestion-2" {
		t.Fatalf("unexpected resumed state: %s attempts=%d owner=%q", job.State(), job.Attempts(), job.LeaseOwner())
	}
}

func TestProcessingJobContentSelectionTransitionsAreFenced(t *testing.T) {
	now := time.Now().UTC()
	job, _ := NewProcessingJob("job-1", "book-1", checksum(1), "config-1", now)
	_ = job.Claim("worker-1", now, time.Minute)

	if err := job.AwaitContentSelection("worker-2", now.Add(time.Second)); !errors.Is(err, ErrLeaseNotOwned) {
		t.Fatalf("expected waiting transition to require lease owner, got %v", err)
	}
	if err := job.ResumeAfterContentSelection("worker-2", now.Add(time.Second), time.Minute); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("expected resume to require awaiting state, got %v", err)
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

func TestRestoreProcessingJobAcceptsAwaitingSelectionWithoutLease(t *testing.T) {
	now := time.Now().UTC()
	job, err := RestoreProcessingJob("job-1", "book-1", checksum(1), "config-1", JobAwaitingSelection, 1, "", time.Time{}, time.Time{}, "", "", [32]byte{}, 0, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if job.State() != JobAwaitingSelection {
		t.Fatalf("state = %q", job.State())
	}
}

func TestRestoreProcessingJobRejectsAwaitingSelectionWithoutPriorClaim(t *testing.T) {
	now := time.Now().UTC()
	_, err := RestoreProcessingJob("job-1", "book-1", checksum(1), "config-1", JobAwaitingSelection, 0, "", time.Time{}, time.Time{}, "", "", [32]byte{}, 0, now, now)
	if !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("expected awaiting state without an attempt to be invalid, got %v", err)
	}
}

func checksum(value byte) [32]byte {
	var sum [32]byte
	sum[0] = value
	return sum
}
