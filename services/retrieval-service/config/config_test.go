package config

import "testing"

func TestLoadRequiresCompletePrivateRuntimeConfiguration(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "http://tei:80")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v1")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")

	configuration, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.GRPCAddress != ":8083" || configuration.QdrantCollection != "evidence_v1" {
		t.Fatalf("unexpected configuration: %#v", configuration)
	}
}

func TestLoadRejectsPublicDependencyURL(t *testing.T) {
	t.Setenv("RETRIEVAL_GRPC_ADDRESS", ":8083")
	t.Setenv("RETRIEVAL_TEI_URL", "https://models.example.com")
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("RETRIEVAL_QDRANT_COLLECTION", "evidence_v1")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", "/run/secrets/dsn")
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", "/run/secrets/qdrant")
	t.Setenv("RETRIEVAL_TLS_CA_FILE", "/run/secrets/ca")
	t.Setenv("RETRIEVAL_TLS_CERT_FILE", "/run/secrets/cert")
	t.Setenv("RETRIEVAL_TLS_KEY_FILE", "/run/secrets/key")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want public dependency URL rejection")
	}
}
