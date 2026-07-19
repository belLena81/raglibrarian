package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUsesBoundedProductionDefaults(t *testing.T) {
	setRequiredEnvironment(t)
	value, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if value.MaximumSourceBytes != 25<<20 || value.MaximumPages != 500 || value.MaximumChunks != 50_000 || value.MaximumManifestBytes != 1<<20 || value.WorkConcurrency != 1 {
		t.Fatalf("unexpected defaults: %#v", value)
	}
	if value.SourceBucket == value.ArtifactBucket {
		t.Fatal("source and artifact buckets must be isolated")
	}
}

func TestLoadRejectsUnsupportedM4ProcessingProfile(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "chunk limit", key: "INGESTION_MAX_CHUNKS", value: "50001"},
		{name: "source envelope", key: "INGESTION_MAX_SOURCE_BYTES", value: "52428800"},
		{name: "page envelope", key: "INGESTION_MAX_PAGES", value: "501"},
		{name: "manifest envelope", key: "INGESTION_MAX_MANIFEST_BYTES", value: "1048577"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setRequiredEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted unsupported %s=%s", test.key, test.value)
			}
		})
	}
}

func TestLoadRejectsTemporaryDirectoryOutsideSandbox(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("INGESTION_TEMP_DIR", "/var/tmp")
	if _, err := Load(); err == nil {
		t.Fatal("expected fail-closed temporary directory validation")
	}
}

func TestLoadRejectsParserMemoryOvercommit(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("INGESTION_WORK_CONCURRENCY", "2")
	t.Setenv("INGESTION_MEMORY_LIMIT_BYTES", "1073741824")
	if _, err := Load(); err == nil {
		t.Fatal("expected parser memory overcommit to fail closed")
	}
}

func TestLoadRejectsTemporaryLimitBelowAcceptedSource(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("INGESTION_MAX_TEMP_BYTES", "1024")
	if _, err := Load(); err == nil {
		t.Fatal("expected temporary storage validation error")
	}
}

func TestLoadCleanupRequiresOnlyCleanupCredentials(t *testing.T) {
	directory := t.TempDir()
	for _, key := range []string{"INGESTION_CLEANUP_POSTGRES_DSN_FILE", "INGESTION_CLEANUP_MINIO_ACCESS_KEY_FILE", "INGESTION_CLEANUP_MINIO_SECRET_KEY_FILE"} {
		path := filepath.Join(directory, key)
		if err := os.WriteFile(path, []byte("cleanup-only-test-value\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	t.Setenv("INGESTION_MINIO_ENDPOINT", "minio:9000")
	t.Setenv("INGESTION_MINIO_INSECURE", "true")
	t.Setenv("INGESTION_ARTIFACT_BUCKET", "ingestion-artifacts")

	value, err := LoadCleanup()
	if err != nil {
		t.Fatal(err)
	}
	if value.DSN == "" || value.MinIOAccessKey == "" || value.ArtifactBucket != "ingestion-artifacts" {
		t.Fatalf("unexpected cleanup config: %#v", value)
	}
}

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	secretKeys := []string{"INGESTION_POSTGRES_DSN_FILE", "INGESTION_RABBITMQ_URI_FILE", "INGESTION_MINIO_ACCESS_KEY_FILE", "INGESTION_MINIO_SECRET_KEY_FILE"}
	for _, key := range secretKeys {
		path := filepath.Join(directory, key)
		if err := os.WriteFile(path, []byte("synthetic-test-value\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	t.Setenv("INGESTION_MINIO_ENDPOINT", "minio:9000")
	t.Setenv("INGESTION_MINIO_INSECURE", "true")
	t.Setenv("INGESTION_SOURCE_BUCKET", "original-books")
	t.Setenv("INGESTION_ARTIFACT_BUCKET", "ingestion-artifacts")
	t.Setenv("INGESTION_TOKENIZER_FILE", filepath.Join(directory, "cl100k.tiktoken"))
}
