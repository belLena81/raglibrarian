package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadUsesBoundedProductionDefaults(t *testing.T) {
	setRequiredEnvironment(t)
	value, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if value.MaximumSourceBytes != 25<<20 || value.MaximumPages != 1000 || value.MaximumChunks != 50_000 || value.MaximumManifestBytes != 1<<20 || value.WorkConcurrency != 1 {
		t.Fatalf("unexpected defaults: %#v", value)
	}
	if value.WorkerReadinessProbeTimeout != 5*time.Second || value.WorkerReadinessRefreshInterval != 5*time.Second ||
		value.WorkerMetricsReadHeaderTimeout != 10*time.Second || value.WorkerMetricsShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected worker runtime defaults: %#v", value)
	}
	if value.MemoryLimitBytes != 2<<30 || value.ParserSandboxMemoryBytes != 1536<<20 {
		t.Fatalf("unexpected parser memory defaults: %#v", value)
	}
	if value.ChunkMaximumTokens != 512 || value.ChunkOverlapTokens != 120 || value.ChunkTargetPages != 2 || value.ChunkMaximumPages != 3 {
		t.Fatalf("unexpected chunk profile defaults: %#v", value)
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
		{name: "page envelope", key: "INGESTION_MAX_PAGES", value: "1001"},
		{name: "manifest envelope", key: "INGESTION_MAX_MANIFEST_BYTES", value: "1048577"},
		{name: "chunk token profile", key: "INGESTION_CHUNK_MAX_TOKENS", value: "513"},
		{name: "chunk overlap profile", key: "INGESTION_CHUNK_OVERLAP_TOKENS", value: "121"},
		{name: "chunk target pages profile", key: "INGESTION_CHUNK_TARGET_PAGES", value: "3"},
		{name: "chunk maximum pages profile", key: "INGESTION_CHUNK_MAX_PAGES", value: "4"},
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

func TestLoadAcceptsAbsolutePDFTextDebugDumpDirectory(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("INGESTION_DEBUG_DUMP_PDFTEXT_DIR", "/tmp/raglibrarian-pdftotext-debug")
	value, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if value.DebugDumpPDFTextDirectory != "/tmp/raglibrarian-pdftotext-debug" {
		t.Fatalf("DebugDumpPDFTextDirectory = %q", value.DebugDumpPDFTextDirectory)
	}
}

func TestLoadRejectsUnsafePDFTextDebugDumpDirectory(t *testing.T) {
	tests := []string{
		"relative/path",
		"/",
		"/tmp/debug\nother",
		"/tmp/debug\tother",
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			setRequiredEnvironment(t)
			t.Setenv("INGESTION_DEBUG_DUMP_PDFTEXT_DIR", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted debug dump dir %q", value)
			}
		})
	}
}

func TestLoadRejectsParserMemoryOvercommit(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("INGESTION_WORK_CONCURRENCY", "2")
	t.Setenv("INGESTION_MEMORY_LIMIT_BYTES", "2147483648")
	if _, err := Load(); err == nil {
		t.Fatal("expected parser memory overcommit to fail closed")
	}
}

func TestLoadRejectsParserSandboxMemoryBelowEPUBSafeDefault(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("INGESTION_PARSER_SANDBOX_MEMORY_BYTES", "805306368")
	if _, err := Load(); err == nil {
		t.Fatal("expected parser sandbox memory below EPUB-safe default to fail closed")
	}
}

func TestLoadRejectsTemporaryLimitBelowAcceptedSource(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("INGESTION_MAX_TEMP_BYTES", "1024")
	if _, err := Load(); err == nil {
		t.Fatal("expected temporary storage validation error")
	}
}

func TestLoadParsesWorkerRuntimePolicy(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("INGESTION_WORKER_READINESS_PROBE_TIMEOUT", "1500ms")
	t.Setenv("INGESTION_WORKER_READINESS_REFRESH_INTERVAL", "3s")
	t.Setenv("INGESTION_WORKER_METRICS_READ_HEADER_TIMEOUT", "6s")
	t.Setenv("INGESTION_WORKER_METRICS_SHUTDOWN_TIMEOUT", "7s")

	value, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if value.WorkerReadinessProbeTimeout != 1500*time.Millisecond ||
		value.WorkerReadinessRefreshInterval != 3*time.Second ||
		value.WorkerMetricsReadHeaderTimeout != 6*time.Second ||
		value.WorkerMetricsShutdownTimeout != 7*time.Second {
		t.Fatalf("unexpected worker runtime policy: %#v", value)
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

func TestLoadDispatcherRequiresOnlyDispatcherCredentials(t *testing.T) {
	directory := t.TempDir()
	for _, key := range []string{"INGESTION_POSTGRES_DSN_FILE", "INGESTION_RABBITMQ_URI_FILE"} {
		path := filepath.Join(directory, key)
		if err := os.WriteFile(path, []byte("dispatcher-only-test-value\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}

	value, err := LoadDispatcher()
	if err != nil {
		t.Fatal(err)
	}
	if value.DSN == "" || value.RabbitURI == "" || value.ResultExchange != "raglibrarian.ingestion.events.v1" {
		t.Fatalf("unexpected dispatcher config: %#v", value)
	}
}

func TestLoadDispatcherRejectsMissingPublisherSecret(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "postgres")
	if err := os.WriteFile(path, []byte("dispatcher-only-test-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INGESTION_POSTGRES_DSN_FILE", path)
	if _, err := LoadDispatcher(); err == nil {
		t.Fatal("LoadDispatcher() accepted a missing publisher secret")
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
