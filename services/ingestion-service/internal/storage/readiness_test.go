package storage

import (
	"context"
	"testing"
)

type readinessStub bool

func (value readinessStub) Ready(context.Context) bool { return bool(value) }

func TestAllReadyFailsWhenArtifactBucketIsUnavailable(t *testing.T) {
	if AllReady(context.Background(), readinessStub(true), readinessStub(false)) {
		t.Fatal("artifact-only dependency loss must fail readiness")
	}
	if !AllReady(context.Background(), readinessStub(true), readinessStub(true)) {
		t.Fatal("both controlled buckets should be ready")
	}
}
