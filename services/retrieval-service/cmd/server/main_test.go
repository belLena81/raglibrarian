package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	retrievalruntime "github.com/belLena81/raglibrarian/services/retrieval-service/internal/runtime"
	"go.uber.org/zap"
)

func TestConfigureSummaryProviderDisablesInvalidConfiguration(t *testing.T) {
	directory := t.TempDir()
	apiKeyFile := filepath.Join(directory, "summary-api-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-api-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := retrievalruntime.NewSummaryProvider(config.Config{
		SummaryLLMBaseURL:           "http://openrouter.ai",
		SummaryLLMModel:             "ohere/north-mini-code:free",
		SummaryLLMAPIKeyFile:        apiKeyFile,
		SummaryLLMMaxOutputTokens:   64,
		SummaryLLMRequestsPerMinute: 1,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSummaryProvider() error = %v", err)
	}
	if provider != nil {
		t.Fatal("NewSummaryProvider() returned a provider for invalid configuration")
	}
}

func TestConfigureSummaryProviderRejectsPermissiveAPIKeyFile(t *testing.T) {
	directory := t.TempDir()
	apiKeyFile := filepath.Join(directory, "summary-api-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-api-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider, err := retrievalruntime.NewSummaryProvider(config.Config{
		SummaryLLMBaseURL:           "https://openrouter.ai",
		SummaryLLMModel:             "ohere/north-mini-code:free",
		SummaryLLMAPIKeyFile:        apiKeyFile,
		SummaryLLMMaxOutputTokens:   64,
		SummaryLLMRequestsPerMinute: 1,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSummaryProvider() error = %v", err)
	}
	if provider != nil {
		t.Fatal("NewSummaryProvider() accepted a permissive API key file")
	}
}
