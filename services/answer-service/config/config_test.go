package config

import (
	"testing"
	"time"
)

func TestLoadUsesSecureBoundedDefaults(t *testing.T) {
	setRequiredEnvironment(t)
	configuration, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Limits.MaximumEvidence != 8 || configuration.Limits.MaximumContextBytes != 32<<10 || configuration.Limits.GeneratorConcurrency != 4 {
		t.Fatalf("unexpected limits: %#v", configuration.Limits)
	}
	if configuration.RequestPolicy.MaximumQuestionCharacters != 2000 || configuration.RequestPolicy.MaximumFilterTags != 20 ||
		configuration.RequestPolicy.MaximumTagCharacters != 64 || configuration.RequestPolicy.MaximumAuthorCharacters != 256 ||
		configuration.RequestPolicy.MaximumResultLimit != 20 {
		t.Fatalf("unexpected request policy: %#v", configuration.RequestPolicy)
	}
	if configuration.Limits.MaximumSummaryRunes != 512 {
		t.Fatalf("MaximumSummaryRunes = %d, want 512", configuration.Limits.MaximumSummaryRunes)
	}
	if configuration.Limits.MaximumFailureDetailRunes != 160 {
		t.Fatalf("MaximumFailureDetailRunes = %d, want 160", configuration.Limits.MaximumFailureDetailRunes)
	}
	if configuration.Generator.MaxResponseBytes != 128<<10 || configuration.Generator.MaxCandidateBytes != 32<<10 {
		t.Fatalf("unexpected provider policy: response=%d candidate=%d", configuration.Generator.MaxResponseBytes, configuration.Generator.MaxCandidateBytes)
	}
	if configuration.Generator.HTTPClientTimeout != 0 {
		t.Fatalf("Provider.HTTPClientTimeout = %s, want 0", configuration.Generator.HTTPClientTimeout)
	}
	if configuration.MetricsMaxHeaderBytes != 16<<10 {
		t.Fatalf("MetricsMaxHeaderBytes = %d, want %d", configuration.MetricsMaxHeaderBytes, 16<<10)
	}
	if configuration.ReadinessProbeTimeout != 2*time.Second || configuration.ReadinessPollInterval != 2*time.Second ||
		configuration.ShutdownTimeout != 3*time.Second || configuration.MetricsReadTimeout != 3*time.Second ||
		configuration.MetricsReadHeaderTimeout != 2*time.Second || configuration.MetricsWriteTimeout != 5*time.Second ||
		configuration.MetricsIdleTimeout != 30*time.Second {
		t.Fatalf("unexpected app runtime policy: %#v", configuration)
	}
}

func TestLoadDefaultsFreeTierProviderRateLimit(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_LLM_MODEL", "inclusionai/ling-3.0-flash:free")
	configuration, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Generator.RequestsPerMinute != 0 {
		t.Fatalf("Provider.RequestsPerMinute = %d, want 0", configuration.Generator.RequestsPerMinute)
	}
	if configuration.Generator.LogErrorBody {
		t.Fatal("Provider.LogErrorBody = true, want false")
	}
}

func TestLoadEnablesProviderErrorBodyLoggingFlag(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_PROVIDER_LOG_ERROR_BODY", "true")
	configuration, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !configuration.Generator.LogErrorBody {
		t.Fatal("Provider.LogErrorBody = false, want true")
	}
}

func TestLoadOverridesProviderPolicy(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_PROVIDER_REQUESTS_PER_MINUTE", "2001")
	t.Setenv("ANSWER_PROVIDER_MAX_RESPONSE_BYTES", "65536")
	t.Setenv("ANSWER_PROVIDER_MAX_CANDIDATE_BYTES", "16384")
	t.Setenv("ANSWER_PROVIDER_HTTP_TIMEOUT", "15s")
	t.Setenv("ANSWER_MAX_SUMMARY_RUNES", "256")
	t.Setenv("ANSWER_MAX_FAILURE_DETAIL_RUNES", "80")
	t.Setenv("ANSWER_MAX_QUESTION_CHARACTERS", "1024")
	t.Setenv("ANSWER_MAX_FILTER_TAGS", "10")
	t.Setenv("ANSWER_MAX_TAG_CHARACTERS", "32")
	t.Setenv("ANSWER_MAX_AUTHOR_CHARACTERS", "128")
	t.Setenv("ANSWER_MAX_RESULT_LIMIT", "7")
	t.Setenv("ANSWER_METRICS_MAX_HEADER_BYTES", "65535")
	configuration, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Generator.MaxResponseBytes != 65536 || configuration.Generator.MaxCandidateBytes != 16384 {
		t.Fatalf("unexpected provider policy: response=%d candidate=%d", configuration.Generator.MaxResponseBytes, configuration.Generator.MaxCandidateBytes)
	}
	if configuration.Generator.RequestsPerMinute != 2001 {
		t.Fatalf("Provider.RequestsPerMinute = %d, want 2001", configuration.Generator.RequestsPerMinute)
	}
	if configuration.MetricsMaxHeaderBytes != 65535 {
		t.Fatalf("MetricsMaxHeaderBytes = %d, want %d", configuration.MetricsMaxHeaderBytes, 65535)
	}
	if configuration.Limits.MaximumSummaryRunes != 256 {
		t.Fatalf("MaximumSummaryRunes = %d, want 256", configuration.Limits.MaximumSummaryRunes)
	}
	if configuration.Limits.MaximumFailureDetailRunes != 80 {
		t.Fatalf("MaximumFailureDetailRunes = %d, want 80", configuration.Limits.MaximumFailureDetailRunes)
	}
	if configuration.RequestPolicy.MaximumQuestionCharacters != 1024 || configuration.RequestPolicy.MaximumFilterTags != 10 ||
		configuration.RequestPolicy.MaximumTagCharacters != 32 || configuration.RequestPolicy.MaximumAuthorCharacters != 128 ||
		configuration.RequestPolicy.MaximumResultLimit != 7 {
		t.Fatalf("unexpected request policy: %#v", configuration.RequestPolicy)
	}
	if configuration.Generator.HTTPClientTimeout != 15*time.Second {
		t.Fatalf("Provider.HTTPClientTimeout = %s, want 15s", configuration.Generator.HTTPClientTimeout)
	}
}

func TestLoadRejectsInsecureProviderAndInvalidBounds(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_LLM_BASE_URL", "http://provider")
	if _, err := Load(); err == nil {
		t.Fatal("insecure provider URL accepted")
	}
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_MAX_EVIDENCE_BYTES", "65536")
	if _, err := Load(); err == nil {
		t.Fatal("per-item limit larger than context accepted")
	}
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_PROVIDER_REQUESTS_PER_MINUTE", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("negative provider rate limit accepted")
	}
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_PROVIDER_REQUESTS_PER_MINUTE", "maybe")
	if _, err := Load(); err == nil {
		t.Fatal("malformed provider rate limit accepted")
	}
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_PROVIDER_MAX_RESPONSE_BYTES", "0")
	if _, err := Load(); err == nil {
		t.Fatal("non-positive provider response limit accepted")
	}
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_PROVIDER_MAX_CANDIDATE_BYTES", "131073")
	if _, err := Load(); err == nil {
		t.Fatal("oversized provider candidate limit accepted")
	}
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_PROVIDER_MAX_RESPONSE_BYTES", "16384")
	t.Setenv("ANSWER_PROVIDER_MAX_CANDIDATE_BYTES", "32768")
	if _, err := Load(); err == nil {
		t.Fatal("provider candidate limit above response limit accepted")
	}
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_PROVIDER_HTTP_TIMEOUT", "-1s")
	if _, err := Load(); err == nil {
		t.Fatal("negative provider HTTP timeout accepted")
	}
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_PROVIDER_HTTP_TIMEOUT", "4m31s")
	if _, err := Load(); err == nil {
		t.Fatal("provider HTTP timeout above provider timeout accepted")
	}
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_METRICS_MAX_HEADER_BYTES", "0")
	if _, err := Load(); err == nil {
		t.Fatal("non-positive metrics header limit accepted")
	}
	setRequiredEnvironment(t)
	t.Setenv("ANSWER_MAX_RESULT_LIMIT", "0")
	if _, err := Load(); err == nil {
		t.Fatal("non-positive request result limit accepted")
	}
}

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"ANSWER_GRPC_ADDR": ":50055", "ANSWER_METRICS_ADDR": ":9096", "ANSWER_RETRIEVAL_GRPC_ADDR": "retrieval-service:50054",
		"ANSWER_LLM_BASE_URL": "https://provider", "ANSWER_LLM_MODEL": "model", "ANSWER_LLM_API_KEY_FILE": "/run/secrets/provider-key",
		"ANSWER_TLS_CA_FILE": "/run/secrets/ca", "ANSWER_TLS_CERT_FILE": "/run/secrets/cert", "ANSWER_TLS_KEY_FILE": "/run/secrets/key",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}
