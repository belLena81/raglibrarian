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
	if configuration.EmbeddingProviderKind != "tei" || configuration.VectorProviderKind != "qdrant" || configuration.SummaryLLMProviderKind != "openai_compatible" {
		t.Fatalf("unexpected provider kinds: %#v", configuration)
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
	if configuration.SummaryLLMOutputMode != "json_or_plain" {
		t.Fatalf("SummaryLLMOutputMode = %q, want json_or_plain", configuration.SummaryLLMOutputMode)
	}
	if configuration.SearchTimeout != 25*time.Second {
		t.Fatalf("SearchTimeout = %s, want 25s", configuration.SearchTimeout)
	}
	if configuration.DependencyTimeout != 25*time.Second {
		t.Fatalf("DependencyTimeout = %s, want 25s", configuration.DependencyTimeout)
	}
	if configuration.SummaryLLMTimeout <= 0 || configuration.SummaryLLMTimeout >= configuration.SearchTimeout {
		t.Fatalf("SummaryLLMTimeout = %s, want a positive timeout below SearchTimeout", configuration.SummaryLLMTimeout)
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
	if configuration.SummaryLLMTimeout <= 0 || configuration.SummaryLLMTimeout >= configuration.SearchTimeout {
		t.Fatalf("SummaryLLMTimeout = %s, want a positive timeout below SearchTimeout", configuration.SummaryLLMTimeout)
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
	if configuration.SummaryLLMTimeout <= 0 || configuration.SummaryLLMTimeout >= configuration.SearchTimeout {
		t.Fatalf("SummaryLLMTimeout = %s, want a positive timeout below SearchTimeout", configuration.SummaryLLMTimeout)
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

func TestLoadOverridesSummaryProviderOutputMode(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_OUTPUT_MODE", " STRICT_JSON ")
	t.Setenv("RETRIEVAL_SEARCH_TIMEOUT", "25s")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.SummaryLLMOutputMode != "strict_json" {
		t.Fatalf("SummaryLLMOutputMode = %q, want strict_json", configuration.SummaryLLMOutputMode)
	}
}

func TestLoadRejectsInvalidSummaryProviderOutputMode(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_OUTPUT_MODE", "plain_text")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid summary provider output mode")
	}
}

func TestLoadRejectsUnknownProviderKinds(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_EMBEDDING_PROVIDER", "voyage")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid embedding provider kind")
	}

	t.Setenv("RETRIEVAL_EMBEDDING_PROVIDER", "tei")
	t.Setenv("RETRIEVAL_VECTOR_PROVIDER", "pinecone")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid vector provider kind")
	}

	t.Setenv("RETRIEVAL_VECTOR_PROVIDER", "qdrant")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_PROVIDER", "anthropic")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid summary provider kind")
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

func TestLoadOverridesReadinessPolicy(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_READY_PROBE_TIMEOUT", "4s")
	t.Setenv("RETRIEVAL_READY_READ_HEADER_TIMEOUT", "5s")
	t.Setenv("RETRIEVAL_READY_IDLE_TIMEOUT", "40s")
	t.Setenv("RETRIEVAL_READY_SHUTDOWN_TIMEOUT", "6s")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.ReadinessProbeTimeout != 4*time.Second || configuration.ReadinessReadHeaderTimeout != 5*time.Second ||
		configuration.ReadinessIdleTimeout != 40*time.Second || configuration.ReadinessShutdownTimeout != 6*time.Second {
		t.Fatalf("unexpected readiness policy: %#v", configuration)
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

func TestLoadRejectsSummaryTimeoutAtOrAboveSearchTimeout(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v2")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	t.Setenv("RETRIEVAL_SEARCH_TIMEOUT", "90s")
	t.Setenv("RETRIEVAL_SUMMARY_LLM_TIMEOUT", "90s")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a summary timeout that was not shorter than the search timeout")
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

func TestLoadWorkerOverridesRuntimePolicy(t *testing.T) {
	setWorkerEnvironment(t)
	t.Setenv("RETRIEVAL_WORKER_DB_PING_TIMEOUT", "7s")
	t.Setenv("RETRIEVAL_WORKER_DEPENDENCY_TIMEOUT", "80s")
	t.Setenv("RETRIEVAL_WORKER_COLLECTION_TIMEOUT", "11s")
	t.Setenv("RETRIEVAL_WORKER_READINESS_INITIAL_DELAY", "2s")
	t.Setenv("RETRIEVAL_WORKER_READINESS_MAX_DELAY", "9s")
	t.Setenv("RETRIEVAL_WORKER_READINESS_MAX_ATTEMPTS", "12")
	t.Setenv("RETRIEVAL_WORKER_READINESS_PROBE_TIMEOUT", "3s")
	t.Setenv("RETRIEVAL_WORKER_RECONNECT_INITIAL_BACKOFF", "2s")
	t.Setenv("RETRIEVAL_WORKER_RECONNECT_MAX_BACKOFF", "35s")
	t.Setenv("RETRIEVAL_WORKER_DISPATCH_INTERVAL", "750ms")
	t.Setenv("RETRIEVAL_WORKER_CLEANUP_INTERVAL", "16m")
	t.Setenv("RETRIEVAL_WORKER_CLEANUP_TIMEOUT", "31s")
	t.Setenv("RETRIEVAL_WORKER_STALE_BATCH_AGE", "17m")
	t.Setenv("RETRIEVAL_WORKER_FAILURE_RECORD_TIMEOUT", "11s")
	t.Setenv("RETRIEVAL_WORKER_PUBLISH_TIMEOUT", "12s")
	t.Setenv("RETRIEVAL_WORKER_RABBITMQ_DIAL_TIMEOUT", "13s")
	t.Setenv("RETRIEVAL_WORKER_RABBITMQ_HEARTBEAT", "14s")
	t.Setenv("RETRIEVAL_WORKER_READY_READ_HEADER_TIMEOUT", "4s")
	t.Setenv("RETRIEVAL_WORKER_READY_IDLE_TIMEOUT", "35s")
	t.Setenv("RETRIEVAL_WORKER_READY_SHUTDOWN_TIMEOUT", "5s")

	configuration, err := LoadWorker()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.DBPingTimeout != 7*time.Second || configuration.DependencyTimeout != 80*time.Second || configuration.CollectionEnsureTimeout != 11*time.Second ||
		configuration.ReadinessInitialDelay != 2*time.Second || configuration.ReadinessMaxDelay != 9*time.Second || configuration.ReadinessMaxAttempts != 12 ||
		configuration.ReadinessProbeTimeout != 3*time.Second || configuration.ReconnectInitialBackoff != 2*time.Second || configuration.ReconnectMaxBackoff != 35*time.Second ||
		configuration.DispatchInterval != 750*time.Millisecond || configuration.CleanupInterval != 16*time.Minute || configuration.CleanupTimeout != 31*time.Second ||
		configuration.StaleBatchAge != 17*time.Minute || configuration.FailureRecordTimeout != 11*time.Second || configuration.PublishTimeout != 12*time.Second ||
		configuration.RabbitDialTimeout != 13*time.Second || configuration.RabbitHeartbeat != 14*time.Second ||
		configuration.ReadinessReadHeaderTimeout != 4*time.Second || configuration.ReadinessIdleTimeout != 35*time.Second || configuration.ReadinessShutdownTimeout != 5*time.Second {
		t.Fatalf("unexpected worker policy: %#v", configuration)
	}
}

func TestLoadWorkerRejectsInvalidRuntimePolicy(t *testing.T) {
	setWorkerEnvironment(t)
	t.Setenv("RETRIEVAL_WORKER_READINESS_INITIAL_DELAY", "11s")
	t.Setenv("RETRIEVAL_WORKER_READINESS_MAX_DELAY", "10s")
	if _, err := LoadWorker(); err == nil {
		t.Fatal("LoadWorker() accepted readiness initial delay above max delay")
	}

	setWorkerEnvironment(t)
	t.Setenv("RETRIEVAL_WORKER_RECONNECT_INITIAL_BACKOFF", "31s")
	t.Setenv("RETRIEVAL_WORKER_RECONNECT_MAX_BACKOFF", "30s")
	if _, err := LoadWorker(); err == nil {
		t.Fatal("LoadWorker() accepted reconnect initial backoff above max backoff")
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
