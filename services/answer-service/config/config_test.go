package config

import "testing"

func TestLoadUsesSecureBoundedDefaults(t *testing.T) {
	setRequiredEnvironment(t)
	configuration, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Limits.MaximumEvidence != 8 || configuration.Limits.MaximumContextBytes != 32<<10 || configuration.Limits.ProviderConcurrency != 4 {
		t.Fatalf("unexpected limits: %#v", configuration.Limits)
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
