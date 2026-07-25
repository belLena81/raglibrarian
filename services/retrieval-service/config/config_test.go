package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadRequiresCompletePrivateRuntimeConfiguration(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.GRPCAddress != ":8083" || configuration.QdrantCollection != "evidence_v2" {
		t.Fatalf("unexpected configuration: %#v", configuration)
	}
}

func TestLoadRejectsPublicDependencyURL(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "https://models.example.com")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want public dependency URL rejection")
	}
}

func TestLoadAcceptsOptionalSummaryProviderConfiguration(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_BASE_URL", "https://llm-provider.example.com")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_MODEL", "summary-model")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_API_KEY_FILE", "/run/secrets/summary-key")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.SummaryLLMBaseURL != "https://llm-provider.example.com" || configuration.SummaryLLMModel != "summary-model" {
		t.Fatalf("unexpected summary provider configuration: %#v", configuration)
	}
	if configuration.SummaryLLMRequestsPerMinute != 15 {
		t.Fatalf("SummaryLLMRequestsPerMinute = %d, want 15", configuration.SummaryLLMRequestsPerMinute)
	}
}

func TestLoadDefaultsSummaryProviderRateLimit(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_MODEL", "openrouter/model:free")
	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.SummaryLLMRequestsPerMinute != 15 {
		t.Fatalf("SummaryLLMRequestsPerMinute = %d, want 15", configuration.SummaryLLMRequestsPerMinute)
	}
}

func TestLoadOverridesSummaryProviderRateLimit(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_REQUESTS_PER_MINUTE", "7")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.SummaryLLMRequestsPerMinute != 7 {
		t.Fatalf("SummaryLLMRequestsPerMinute = %d, want 7", configuration.SummaryLLMRequestsPerMinute)
	}
}

func TestLoadWorkerBoundsServerlessInvocationTimeout(t *testing.T) {
	setWorkerEnvironment(t)
	t.Setenv("RETRIEVAL_SERVERLESS_INVOCATION_TIMEOUT", "45s")
	configuration, err := LoadWorker()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.ServerlessInvocationTimeout != 45*time.Second {
		t.Fatalf("ServerlessInvocationTimeout = %s", configuration.ServerlessInvocationTimeout)
	}

	setWorkerEnvironment(t)
	t.Setenv("RETRIEVAL_SERVERLESS_INVOCATION_TIMEOUT", "1s")
	if _, err := LoadWorker(); err == nil {
		t.Fatal("LoadWorker accepted too-short serverless timeout")
	}
}

func TestLoadWorkerRequiresMetricsAddress(t *testing.T) {
	setWorkerEnvironment(t)
	t.Setenv("RETRIEVAL_METRICS_ADDR", "")
	if _, err := LoadWorker(); err == nil {
		t.Fatal("LoadWorker() accepted a missing metrics address")
	}
}

func TestLoadWorkerDefaultsFreeTierTEIRateLimit(t *testing.T) {
	setWorkerEnvironment(t)
	t.Setenv("RETRIEVAL_TEI_REQUESTS_PER_SECOND", "")
	configuration, err := LoadWorker()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.TEIRequestsPerSecond != 0 {
		t.Fatalf("TEIRequestsPerSecond = %d, want 0", configuration.TEIRequestsPerSecond)
	}
}

func setWorkerEnvironment(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	for _, key := range []string{
		"RETRIEVAL_POSTGRES_DSN_FILE",
		"RETRIEVAL_RABBITMQ_CONSUMER_URI_FILE",
		"RETRIEVAL_RABBITMQ_PUBLISHER_URI_FILE",
		"RETRIEVAL_MINIO_ACCESS_KEY_FILE",
		"RETRIEVAL_MINIO_SECRET_KEY_FILE",
		"RETRIEVAL_QDRANT_API_KEY_FILE",
	} {
		path := filepath.Join(directory, key)
		if err := os.WriteFile(path, []byte("synthetic-test-value\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	t.Setenv("RETRIEVAL_PROCESSING_MODE", "worker")
	t.Setenv("RETRIEVAL_INDEX_PROFILE", "m7-pdf-epub-v1")
	t.Setenv("RETRIEVAL_MINIO_ENDPOINT", "minio:9000")
	t.Setenv("RETRIEVAL_MINIO_INSECURE", "true")
	t.Setenv("RETRIEVAL_ARTIFACT_BUCKET", "retrieval-artifacts")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_METRICS_ADDR", "127.0.0.1:9094")
}
