package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
)

func TestExistingActiveLeaseDefersDeliveryUntilRecoveryBoundary(t *testing.T) {
	now := time.Now().UTC()
	job, err := domain.NewProcessingJob("job-1", "book-1", [32]byte{1}, "config", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = job.Claim("worker-1", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	claimable, decisionErr := existingJobDecision(job, now.Add(time.Second))
	if claimable || !errors.Is(decisionErr, application.ErrProcessingDeferred) {
		t.Fatalf("active lease must defer delivery: claimable=%v err=%v", claimable, decisionErr)
	}
	var deferred application.DeferredError
	if !errors.As(decisionErr, &deferred) || !deferred.RetryAt.Equal(job.LeaseExpiresAt()) {
		t.Fatalf("unexpected recovery time: %v", decisionErr)
	}
}

func TestExpiredLeaseCanBeReclaimed(t *testing.T) {
	now := time.Now().UTC()
	job, _ := domain.NewProcessingJob("job-1", "book-1", [32]byte{1}, "config", now)
	_ = job.Claim("worker-1", now, time.Second)
	claimable, err := existingJobDecision(job, now.Add(2*time.Second))
	if err != nil || !claimable {
		t.Fatalf("expired lease should be claimable: claimable=%v err=%v", claimable, err)
	}
}
