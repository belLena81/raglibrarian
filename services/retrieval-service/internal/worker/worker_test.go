package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/rabbitmq/amqp091-go"
)

func TestBoundedManifestPolicyAcceptsConfiguredWorkerDefaults(t *testing.T) {
	configuration := config.WorkerConfig{
		ManifestMaxPages:                1000,
		ManifestMaxShards:               1024,
		ManifestMaxShardCompressedBytes: 128 << 20,
		ManifestMaxShardExpandedBytes:   256 << 20,
		ManifestMaxShardChunks:          1024,
		ManifestMaxTotalChunks:          200000,
		ManifestMaxExpandedBytes:        8 << 30,
	}

	policy, err := boundedManifestPolicy(configuration)
	if err != nil {
		t.Fatalf("boundedManifestPolicy() error = %v", err)
	}
	if policy.MaxPages != uint32(configuration.ManifestMaxPages) {
		t.Fatalf("MaxPages = %d, want %d", policy.MaxPages, configuration.ManifestMaxPages)
	}
	if policy.MaxShardChunks != uint32(configuration.ManifestMaxShardChunks) {
		t.Fatalf("MaxShardChunks = %d, want %d", policy.MaxShardChunks, configuration.ManifestMaxShardChunks)
	}
	if policy.MaxTotalChunks != uint32(configuration.ManifestMaxTotalChunks) {
		t.Fatalf("MaxTotalChunks = %d, want %d", policy.MaxTotalChunks, configuration.ManifestMaxTotalChunks)
	}
}

func TestBoundedManifestPolicyRejectsUint32Overflow(t *testing.T) {
	configuration := config.WorkerConfig{
		ManifestMaxPages:                maximumManifestBound + 1,
		ManifestMaxShards:               1,
		ManifestMaxShardCompressedBytes: 1,
		ManifestMaxShardExpandedBytes:   1,
		ManifestMaxShardChunks:          1,
		ManifestMaxTotalChunks:          1,
		ManifestMaxExpandedBytes:        1,
	}

	if _, err := boundedManifestPolicy(configuration); err == nil {
		t.Fatal("boundedManifestPolicy() error = nil")
	}
}

func TestProcessOneRejectsUnknownQueueBeforeUsingRuntimeDependencies(t *testing.T) {
	runtime := &Runtime{}
	err := runtime.ProcessOne(context.Background(), "unknown", "event", []byte("payload"))
	if !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("ProcessOne() error = %v, want invalid event", err)
	}
}

func TestProcessOneDeliveryLeavesCancelledDeliveryUnsettled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&Runtime{}).ProcessOneDelivery(ctx, nil, metadataQueue, amqp091.Delivery{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessOneDelivery() error = %v, want cancelled", err)
	}
}

func TestProcessOneRejectsWrongEventTypeBeforeUsingRuntimeDependencies(t *testing.T) {
	runtime := &Runtime{}
	err := runtime.ProcessOne(context.Background(), metadataQueue, "unexpected", []byte("payload"))
	if !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("ProcessOne() error = %v, want invalid event", err)
	}
}

func TestAwaitReadinessReturnsNilWhenDependencyBecomesReady(t *testing.T) {
	attempts := 0
	err := awaitReadiness(context.Background(), func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("not ready yet")
		}
		return nil
	}, 5, time.Millisecond, 4*time.Millisecond, 2*time.Millisecond)
	if err != nil {
		t.Fatalf("awaitReadiness() error = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestAwaitReadinessReturnsContextCancelationError(t *testing.T) {
	attempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := awaitReadiness(ctx, func(context.Context) error {
		attempts++
		return errors.New("not ready")
	}, 5, time.Millisecond, 4*time.Millisecond, 2*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("awaitReadiness() error = %v, want %v", err, context.Canceled)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestAwaitReadinessRejectsInvalidProbe(t *testing.T) {
	err := awaitReadiness(context.Background(), nil, 5, time.Millisecond, 4*time.Millisecond, 2*time.Millisecond)
	if err == nil {
		t.Fatal("awaitReadiness() error = nil, want invalid readiness probe")
	}
	err = awaitReadiness(context.Background(), func(context.Context) error { return errors.New("not ready") }, 0, time.Millisecond, 4*time.Millisecond, 2*time.Millisecond)
	if err == nil {
		t.Fatal("awaitReadiness() error = nil, want invalid readiness probe")
	}
}

func TestRejectionReasonMapsFailureCategoryAndCommonErrors(t *testing.T) {
	if got := rejectionReason(application.Failure(domain.FailureEmbeddingUnavailable, errors.New("tei unavailable"))); got != "embedding_unavailable" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "embedding_unavailable")
	}
	if got := rejectionReason(application.Failure(domain.FailureIndexingTimeout, errors.New("tei timeout"))); got != "indexing_timeout" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "indexing_timeout")
	}
	if got := rejectionReason(application.Failure(domain.FailureManifestIntegrity, errors.New("invalid chunk"))); got != "manifest_integrity" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "manifest_integrity")
	}
	if got := rejectionReason(application.Failure(domain.FailureResourceLimit, errors.New("too large"))); got != "resource_limit_exceeded" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "resource_limit_exceeded")
	}
	if got := rejectionReason(application.ErrConflictingEvent); got != "conflicting_event" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "conflicting_event")
	}
	if got := rejectionReason(application.ErrInvalidEvent); got != "invalid_event" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "invalid_event")
	}
	if got := rejectionReason(errors.New("other failure")); got != "unknown_failure" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "unknown_failure")
	}
}
