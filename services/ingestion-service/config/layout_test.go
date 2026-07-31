package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLayoutUsesSafeParserMemoryDefaults(t *testing.T) {
	setRequiredLayoutEnvironment(t)
	value, err := LoadLayout()
	if err != nil {
		t.Fatal(err)
	}
	if value.WorkConcurrency != 1 || value.MemoryLimitBytes != 2<<30 ||
		value.ParserSandboxMemoryBytes != 1536<<20 || value.ParserRuntimeHeadroomBytes != 256<<20 {
		t.Fatalf("unexpected layout memory policy: %#v", value)
	}
}

func TestLoadLayoutRejectsParserMemoryOvercommit(t *testing.T) {
	setRequiredLayoutEnvironment(t)
	t.Setenv("LAYOUT_WORK_CONCURRENCY", "2")
	if _, err := LoadLayout(); err == nil {
		t.Fatal("layout parser memory overcommit accepted")
	}
}

func TestLoadLayoutRejectsMemoryLimitBelowParserBudget(t *testing.T) {
	setRequiredLayoutEnvironment(t)
	t.Setenv("LAYOUT_MEMORY_LIMIT_BYTES", "1610612736")
	if _, err := LoadLayout(); err == nil {
		t.Fatal("undersized layout memory limit accepted")
	}
}

func setRequiredLayoutEnvironment(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	write := func(name, value string) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Setenv("LAYOUT_RABBITMQ_URI_FILE", write("rabbit-uri", "amqp://layout:secret@rabbitmq:5672/"))
	t.Setenv("LAYOUT_MINIO_ACCESS_KEY_FILE", write("minio-access", "layout-access"))
	t.Setenv("LAYOUT_MINIO_SECRET_KEY_FILE", write("minio-secret", "layout-secret"))
	t.Setenv("INGESTION_MINIO_ENDPOINT", "minio:9000")
}
