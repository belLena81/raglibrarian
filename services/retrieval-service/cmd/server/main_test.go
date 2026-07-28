package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"go.uber.org/zap"
)

func TestConfigureSummaryProviderDisablesInvalidConfiguration(t *testing.T) {
	directory := t.TempDir()
	apiKeyFile := filepath.Join(directory, "summary-api-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-api-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := configureSummaryProvider(config.Config{
		SummaryLLMProviderKind:      "openai_compatible",
		SummaryLLMBaseURL:           "http://openrouter.ai",
		SummaryLLMModel:             "ohere/north-mini-code:free",
		SummaryLLMAPIKeyFile:        apiKeyFile,
		SummaryLLMMaxOutputTokens:   64,
		SummaryLLMRequestsPerMinute: 1,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("configureSummaryProvider() error = %v", err)
	}
	if provider != nil {
		t.Fatal("configureSummaryProvider() returned a provider for invalid configuration")
	}
}

func TestConfigureSummaryProviderRejectsPermissiveAPIKeyFile(t *testing.T) {
	directory := t.TempDir()
	apiKeyFile := filepath.Join(directory, "summary-api-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-api-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider, err := configureSummaryProvider(config.Config{
		SummaryLLMProviderKind:      "openai_compatible",
		SummaryLLMBaseURL:           "https://openrouter.ai",
		SummaryLLMModel:             "ohere/north-mini-code:free",
		SummaryLLMAPIKeyFile:        apiKeyFile,
		SummaryLLMMaxOutputTokens:   64,
		SummaryLLMRequestsPerMinute: 1,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("configureSummaryProvider() error = %v", err)
	}
	if provider != nil {
		t.Fatal("configureSummaryProvider() accepted a permissive API key file")
	}
}
