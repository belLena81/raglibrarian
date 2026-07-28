package main

import (
	"os"
	"path/filepath"
	"testing"

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
