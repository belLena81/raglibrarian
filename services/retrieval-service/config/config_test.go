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
	t.Setenv("RETRIEVAL_SEARCH_TIMEOUT", "25s")

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
	t.Setenv("RETRIEVAL_SEARCH_TIMEOUT", "25s")
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
	t.Setenv("RETRIEVAL_SEARCH_TIMEOUT", "25s")

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
	if configuration.SummaryLLMMaxCalls != 100 {
		t.Fatalf("SummaryLLMMaxCalls = %d, want 100", configuration.SummaryLLMMaxCalls)
	}
	if configuration.SummaryLLMMaxOutputTokens != 64 {
		t.Fatalf("SummaryLLMMaxOutputTokens = %d, want 64", configuration.SummaryLLMMaxOutputTokens)
	}
	if configuration.SearchTimeout != 25*time.Second {
		t.Fatalf("SearchTimeout = %s, want 25s", configuration.SearchTimeout)
	}
	if configuration.DependencyTimeout != 25*time.Second {
		t.Fatalf("DependencyTimeout = %s, want 25s", configuration.DependencyTimeout)
	}
	if configuration.SummaryLLMTimeout != 25*time.Second {
		t.Fatalf("SummaryLLMTimeout = %s, want 25s", configuration.SummaryLLMTimeout)
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
	t.Setenv("RETRIEVAL_SEARCH_TIMEOUT", "2m")
	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.SummaryLLMRequestsPerMinute != 15 {
		t.Fatalf("SummaryLLMRequestsPerMinute = %d, want 15", configuration.SummaryLLMRequestsPerMinute)
	}
	if configuration.SummaryLLMMaxCalls != 100 {
		t.Fatalf("SummaryLLMMaxCalls = %d, want 100", configuration.SummaryLLMMaxCalls)
	}
	if configuration.SearchTimeout != 2*time.Minute {
		t.Fatalf("SearchTimeout = %s, want 2m", configuration.SearchTimeout)
	}
	if configuration.DependencyTimeout != 2*time.Minute {
		t.Fatalf("DependencyTimeout = %s, want 2m", configuration.DependencyTimeout)
	}
	if configuration.SummaryLLMTimeout != 2*time.Minute {
		t.Fatalf("SummaryLLMTimeout = %s, want 2m", configuration.SummaryLLMTimeout)
	}
}

func TestLoadDefaultsSearchTimeoutAndMinimumScore(t *testing.T) {
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
	if configuration.SearchTimeout != 2*time.Minute {
		t.Fatalf("SearchTimeout = %s, want 2m", configuration.SearchTimeout)
	}
	if configuration.DependencyTimeout != 2*time.Minute {
		t.Fatalf("DependencyTimeout = %s, want 2m", configuration.DependencyTimeout)
	}
	if configuration.SummaryLLMTimeout != 2*time.Minute {
		t.Fatalf("SummaryLLMTimeout = %s, want 2m", configuration.SummaryLLMTimeout)
	}
	if configuration.MinimumSearchScore != 0.6 {
		t.Fatalf("MinimumSearchScore = %g, want 0.6", configuration.MinimumSearchScore)
	}
	if configuration.SummaryLLMMaxCalls != 100 {
		t.Fatalf("SummaryLLMMaxCalls = %d, want 100", configuration.SummaryLLMMaxCalls)
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
	t.Setenv("RETRIEVAL_SEARCH_TIMEOUT", "25s")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.SummaryLLMRequestsPerMinute != 7 {
		t.Fatalf("SummaryLLMRequestsPerMinute = %d, want 7", configuration.SummaryLLMRequestsPerMinute)
	}
}

func TestLoadOverridesSummaryProviderMaxCalls(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_MAX_CALLS", "2")
	t.Setenv("RETRIEVAL_SEARCH_TIMEOUT", "25s")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.SummaryLLMMaxCalls != 2 {
		t.Fatalf("SummaryLLMMaxCalls = %d, want 2", configuration.SummaryLLMMaxCalls)
	}
}

func TestLoadOverridesSummaryProviderMaxOutputTokens(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_MAX_OUTPUT_TOKENS", "48")
	t.Setenv("RETRIEVAL_SEARCH_TIMEOUT", "25s")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.SummaryLLMMaxOutputTokens != 48 {
		t.Fatalf("SummaryLLMMaxOutputTokens = %d, want 48", configuration.SummaryLLMMaxOutputTokens)
	}
}

func TestLoadOverridesRetrievalTimeouts(t *testing.T) {
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
	t.Setenv("RETRIEVAL_SEARCH_TIMEOUT", "90s")
	t.Setenv("RETRIEVAL_DEPENDENCY_TIMEOUT", "45s")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_TIMEOUT", "30s")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.SearchTimeout != 90*time.Second {
		t.Fatalf("SearchTimeout = %s, want 90s", configuration.SearchTimeout)
	}
	if configuration.DependencyTimeout != 45*time.Second {
		t.Fatalf("DependencyTimeout = %s, want 45s", configuration.DependencyTimeout)
	}
	if configuration.SummaryLLMTimeout != 30*time.Second {
		t.Fatalf("SummaryLLMTimeout = %s, want 30s", configuration.SummaryLLMTimeout)
	}
}

func TestLoadRejectsInvalidThrottleConfiguration(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_TEI_REQUESTS_PER_SECOND", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted malformed TEI throttle configuration")
	}
}

func TestLoadParsesTEIRawResponseDiagnostics(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_TEI_LOG_RAW_RESPONSE", "true")
	t.Setenv("RETRIEVAL_TEI_LOG_RAW_RESPONSE_MAX_BYTES", "1024")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !configuration.TEILogRawResponse || configuration.TEILogRawResponseMaxBytes != 1024 {
		t.Fatalf("unexpected TEI diagnostics config: %#v", configuration)
	}
}

func TestLoadRejectsInvalidTEIRawResponseDiagnostics(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{key: "RETRIEVAL_TEI_LOG_RAW_RESPONSE", value: "sometimes"},
		{key: "RETRIEVAL_TEI_LOG_RAW_RESPONSE_MAX_BYTES", value: "65537"},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
			t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
			t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
			t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
			t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
			t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
			t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
			t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
			t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
			t.Setenv(test.key, test.value)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted %s=%s", test.key, test.value)
			}
		})
	}
}

func TestLoadRejectsInvalidMinimumSearchScore(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_MINIMUM_SEARCH_SCORE", "NaN")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted NaN minimum search score")
	}

	t.Setenv("RETRIEVAL_MINIMUM_SEARCH_SCORE", "0")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted zero minimum search score")
	}

	t.Setenv("RETRIEVAL_MINIMUM_SEARCH_SCORE", "+Inf")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted infinite minimum search score")
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

func TestLoadWorkerAcceptsDedicatedMetricsAddress(t *testing.T) {
	setWorkerEnvironment(t)
	t.Setenv("RETRIEVAL_METRICS_ADDR", "")
	t.Setenv("RETRIEVAL_WORKER_METRICS_ADDR", "127.0.0.1:9095")

	configuration, err := LoadWorker()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.MetricsAddress != "127.0.0.1:9095" {
		t.Fatalf("MetricsAddress = %q", configuration.MetricsAddress)
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

func TestLoadWorkerParsesTEIRawResponseDiagnostics(t *testing.T) {
	setWorkerEnvironment(t)
	t.Setenv("RETRIEVAL_TEI_LOG_RAW_RESPONSE", "true")
	t.Setenv("RETRIEVAL_TEI_LOG_RAW_RESPONSE_MAX_BYTES", "1024")
	configuration, err := LoadWorker()
	if err != nil {
		t.Fatal(err)
	}
	if !configuration.TEILogRawResponse || configuration.TEILogRawResponseMaxBytes != 1024 {
		t.Fatalf("unexpected TEI diagnostics config: %#v", configuration)
	}
}

func TestLoadWorkerRejectsInvalidTEIRawResponseDiagnostics(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{key: "RETRIEVAL_TEI_LOG_RAW_RESPONSE", value: "sometimes"},
		{key: "RETRIEVAL_TEI_LOG_RAW_RESPONSE_MAX_BYTES", value: "65537"},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			setWorkerEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := LoadWorker(); err == nil {
				t.Fatalf("LoadWorker() accepted %s=%s", test.key, test.value)
			}
		})
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
	t.Setenv("RETRIEVAL_INDEX_PROFILE", "m8-bge-v1")
	t.Setenv("RETRIEVAL_MINIO_ENDPOINT", "minio:9000")
	t.Setenv("RETRIEVAL_MINIO_INSECURE", "true")
	t.Setenv("RETRIEVAL_ARTIFACT_BUCKET", "retrieval-artifacts")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_METRICS_ADDR", "127.0.0.1:9094")
}
