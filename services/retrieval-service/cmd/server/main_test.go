package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	retrievalruntime "github.com/belLena81/raglibrarian/services/retrieval-service/internal/runtime"
	"go.uber.org/zap"
)

func TestConfigureEvidenceAssessorDisablesInvalidConfiguration(t *testing.T) {
	directory := t.TempDir()
	apiKeyFile := filepath.Join(directory, "summary-api-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-api-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assessor, err := retrievalruntime.NewEvidenceAssessor(config.EvidenceAssessorConfig{
		BaseURL:           "http://openrouter.ai",
		Model:             "ohere/north-mini-code:free",
		APIKeyFile:        apiKeyFile,
		MaxOutputTokens:   64,
		RequestsPerMinute: 1,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewEvidenceAssessor() error = %v", err)
	}
	if assessor != nil {
		t.Fatal("NewEvidenceAssessor() returned an assessor for invalid configuration")
	}
}

func TestConfigureEvidenceAssessorRejectsPermissiveAPIKeyFile(t *testing.T) {
	directory := t.TempDir()
	apiKeyFile := filepath.Join(directory, "summary-api-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-api-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assessor, err := retrievalruntime.NewEvidenceAssessor(config.EvidenceAssessorConfig{
		BaseURL:           "https://openrouter.ai",
		Model:             "ohere/north-mini-code:free",
		APIKeyFile:        apiKeyFile,
		MaxOutputTokens:   64,
		RequestsPerMinute: 1,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewEvidenceAssessor() error = %v", err)
	}
	if assessor != nil {
		t.Fatal("NewEvidenceAssessor() accepted a permissive API key file")
	}
}

func TestReadSummaryCacheHMACKeyRequiresLongPrivateSecret(t *testing.T) {
	directory := t.TempDir()
	privateKeyFile := filepath.Join(directory, "summary-cache-hmac")
	if err := os.WriteFile(privateKeyFile, []byte("0123456789abcdef0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := readSummaryCacheHMACKey(privateKeyFile)
	if err != nil || string(key) != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("readSummaryCacheHMACKey() = %q, %v", key, err)
	}

	permissiveKeyFile := filepath.Join(directory, "permissive-summary-cache-hmac")
	if err = os.WriteFile(permissiveKeyFile, []byte("0123456789abcdef0123456789abcdef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = readSummaryCacheHMACKey(permissiveKeyFile); err == nil {
		t.Fatal("readSummaryCacheHMACKey() accepted permissive key file")
	}

	shortKeyFile := filepath.Join(directory, "short-summary-cache-hmac")
	if err = os.WriteFile(shortKeyFile, []byte("too-short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readSummaryCacheHMACKey(shortKeyFile); err == nil {
		t.Fatal("readSummaryCacheHMACKey() accepted short key")
	}
}

func TestCleanupExpiredAssessmentCacheDrainsBoundedBatches(t *testing.T) {
	store := &stubSummaryCacheCleanupStore{results: []int{3, 3, 1}}
	deleted, err := cleanupExpiredAssessmentCache(context.Background(), store, 3)
	if err != nil || deleted != 7 || store.calls != 3 {
		t.Fatalf("cleanupExpiredAssessmentCache() = %d, %v calls=%d, want 7,nil,3", deleted, err, store.calls)
	}
}

func TestCleanupExpiredAssessmentCacheStopsOnError(t *testing.T) {
	store := &stubSummaryCacheCleanupStore{results: []int{3, 0}, errAt: 2}
	deleted, err := cleanupExpiredAssessmentCache(context.Background(), store, 3)
	if !errors.Is(err, errCleanupUnavailable) || deleted != 3 || store.calls != 2 {
		t.Fatalf("cleanupExpiredAssessmentCache() = %d, %v calls=%d", deleted, err, store.calls)
	}
}

func TestServeSummaryCacheCleanupRunsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &stubSummaryCacheCleanupStore{results: []int{0}, called: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveSummaryCacheCleanup(ctx, zap.NewNop(), store, time.Hour, time.Second, 10)
	}()
	select {
	case <-store.called:
	case <-time.After(time.Second):
		t.Fatal("serveSummaryCacheCleanup() did not run startup cleanup")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveSummaryCacheCleanup() did not stop after cancellation")
	}
}

var errCleanupUnavailable = errors.New("cleanup unavailable")

type stubSummaryCacheCleanupStore struct {
	results []int
	errAt   int
	calls   int
	called  chan struct{}
}

func (s *stubSummaryCacheCleanupStore) DeleteExpiredAssessmentCache(context.Context, int) (int, error) {
	s.calls++
	if s.called != nil {
		s.called <- struct{}{}
	}
	if s.errAt == s.calls {
		return 0, errCleanupUnavailable
	}
	return s.results[s.calls-1], nil
}
