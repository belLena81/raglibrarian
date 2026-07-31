package repository

import (
	"crypto/sha256"
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

func TestActiveLeaseRecoveryDispatchIsNotPublishedBeforeLongLeaseExpires(t *testing.T) {
	now := time.Now().UTC()
	job, err := domain.NewProcessingJob("job-1", "book-1", [32]byte{1}, "config", now)
	if err != nil {
		t.Fatal(err)
	}
	lease := 13 * time.Minute
	if err = job.Claim("worker-1", now, lease); err != nil {
		t.Fatal(err)
	}
	_, decisionErr := existingJobDecision(job, now.Add(time.Second))
	nextAttemptAt, deferred := recoveryDispatchTime(decisionErr)
	if !deferred || !nextAttemptAt.Equal(job.LeaseExpiresAt()) {
		t.Fatalf("recovery dispatch must remain pending until lease expiry: at=%v err=%v", nextAttemptAt, decisionErr)
	}
	if nextAttemptAt.Sub(now) <= 2*time.Minute {
		t.Fatal("regression requires a lease beyond the broker's longest retry TTL")
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

func TestAwaitingSelectionCannotBeReclaimedByUploadRedelivery(t *testing.T) {
	now := time.Now().UTC()
	job, _ := domain.NewProcessingJob("job-1", "book-1", [32]byte{1}, "config", now)
	_ = job.Claim("worker-1", now, time.Minute)
	_ = job.AwaitContentSelection("worker-1", now.Add(time.Second))
	claimable, err := existingJobDecision(job, now.Add(2*time.Second))
	if err != nil || claimable {
		t.Fatalf("awaiting job must wait for a selection result: claimable=%v err=%v", claimable, err)
	}
}

func TestContentSelectionRecordEnforcesBrokerAndLifecycleBounds(t *testing.T) {
	payload := []byte{1}
	valid := application.ContentSelectionRecord{
		EventID:                 "selection-result-1",
		RequestID:               "selection-request-1",
		JobID:                   "job-1",
		BookID:                  "book-1",
		PayloadDigest:           sha256.Sum256(payload),
		Payload:                 payload,
		LifecycleVersion:        1,
		ReceivedAt:              time.Now().UTC(),
		SourceSHA256:            [32]byte{1},
		ProcessingProfileDigest: [32]byte{2},
	}
	if !validContentSelectionRecord(valid) {
		t.Fatal("valid bounded content-selection record was rejected")
	}
	oversized := valid
	oversized.Payload = make([]byte, 262145)
	if validContentSelectionRecord(oversized) {
		t.Fatal("oversized content-selection payload was accepted")
	}
	invalidLifecycle := valid
	invalidLifecycle.LifecycleVersion = 0
	if validContentSelectionRecord(invalidLifecycle) {
		t.Fatal("invalid content-selection lifecycle was accepted")
	}
	tampered := valid
	tampered.Payload = []byte{2}
	if validContentSelectionRecord(tampered) {
		t.Fatal("content-selection payload with a mismatched digest was accepted")
	}
}
