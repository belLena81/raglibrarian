package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setRequired(t *testing.T) {
	t.Helper()
	secretDir := t.TempDir()
	writeSecret := func(name, value string) string {
		t.Helper()
		path := filepath.Join(secretDir, name)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Setenv("CATALOG_POSTGRES_DSN_FILE", writeSecret("postgres_dsn", "postgres://catalog:test@postgres:5432/catalog"))
	t.Setenv("CATALOG_MINIO_ACCESS_KEY_FILE", writeSecret("minio_access_key", "catalog-access"))
	t.Setenv("CATALOG_MINIO_SECRET_KEY_FILE", writeSecret("minio_secret_key", "catalog-secret"))
	t.Setenv("CATALOG_RABBITMQ_URI_FILE", writeSecret("rabbitmq_uri", "amqp://catalog:test@rabbitmq:5672/"))
	t.Setenv("CATALOG_INGESTION_RABBITMQ_URI_FILE", writeSecret("ingestion_rabbitmq_uri", "amqp://ingestion:test@rabbitmq:5672/"))
	t.Setenv("CATALOG_RETRIEVAL_RABBITMQ_URI_FILE", writeSecret("retrieval_rabbitmq_uri", "amqp://retrieval:test@rabbitmq:5672/"))
	t.Setenv("INTERNAL_TLS_CA_FILE", "/ca")
	t.Setenv("CATALOG_TLS_CERT_FILE", "/cert")
	t.Setenv("CATALOG_TLS_KEY_FILE", "/key")
	t.Setenv("CATALOG_MINIO_ENDPOINT", "minio:9000")
	t.Setenv("CATALOG_MINIO_BUCKET", "catalog")
	t.Setenv("CATALOG_MINIO_INSECURE", "false")
	t.Setenv("RUN_AS_UID", "65532")
	t.Setenv("RUN_AS_GID", "65532")
}

func TestCatalogBounds(t *testing.T) {
	t.Setenv("CATALOG_MAX_UPLOAD_BYTES", "26214400")
	bytes, err := fixedInt64("CATALOG_MAX_UPLOAD_BYTES", 25<<20)
	if err != nil || bytes != 25<<20 {
		t.Fatalf("bytes = %d, err = %v", bytes, err)
	}
	t.Setenv("CATALOG_MAX_UPLOAD_BYTES", "52428800")
	if _, err := fixedInt64("CATALOG_MAX_UPLOAD_BYTES", 25<<20); err == nil {
		t.Fatal("expected unsupported upload envelope error")
	}
	t.Setenv("CATALOG_UPLOAD_CONCURRENCY", "17")
	if _, err := boundedInt("CATALOG_UPLOAD_CONCURRENCY", 2, 16); err == nil {
		t.Fatal("expected concurrency error")
	}
	t.Setenv("CATALOG_PREVIEW_CONCURRENCY", "3")
	if value, err := boundedInt("CATALOG_PREVIEW_CONCURRENCY", 2, 16); err != nil || value != 3 {
		t.Fatalf("preview concurrency = %d, err = %v", value, err)
	}
	t.Setenv("CATALOG_PREVIEW_TIMEOUT", "5s")
	if value, err := boundedDuration("CATALOG_PREVIEW_TIMEOUT", time.Second, 30*time.Second, 5*time.Second); err != nil || value != 5*time.Second {
		t.Fatalf("preview timeout = %v, err = %v", value, err)
	}
}

func TestPrivateMetricsAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:9092", "0.0.0.0:9092", "10.0.0.10:9092", "[::1]:9092", "[::]:9092"} {
		if _, err := privateMetricsAddress(address); err != nil {
			t.Errorf("privateMetricsAddress(%q): %v", address, err)
		}
	}
	if _, err := privateMetricsAddress("8.8.8.8:9092"); err == nil {
		t.Fatal("expected public address rejection")
	}
	if _, err := privateMetricsAddress(":9092"); err == nil {
		t.Fatal("expected wildcard address rejection")
	}
}

func TestStrictBool(t *testing.T) {
	t.Setenv("CATALOG_MINIO_INSECURE", "false")
	if value, err := strictBool("CATALOG_MINIO_INSECURE", false); err != nil || value {
		t.Fatalf("strictBool() = %v, %v", value, err)
	}
	t.Setenv("CATALOG_MINIO_INSECURE", "1")
	if _, err := strictBool("CATALOG_MINIO_INSECURE", false); err == nil {
		t.Fatal("expected non-boolean value rejection")
	}
}

func TestMinIOEndpoint(t *testing.T) {
	for _, endpoint := range []string{"minio:9000", "storage.internal", "[::1]:9000"} {
		if err := validateMinIOEndpoint(endpoint); err != nil {
			t.Errorf("validateMinIOEndpoint(%q): %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"https://minio:9000", "minio:9000/path", "user@minio:9000", ""} {
		if err := validateMinIOEndpoint(endpoint); err == nil {
			t.Errorf("expected endpoint %q rejection", endpoint)
		}
	}
}

func TestBoundedDuration(t *testing.T) {
	t.Setenv("CATALOG_RECONCILE_INTERVAL", "15m")
	if value, err := boundedDuration("CATALOG_RECONCILE_INTERVAL", time.Minute, 24*time.Hour, time.Hour); err != nil || value != 15*time.Minute {
		t.Fatalf("boundedDuration() = %v, %v", value, err)
	}
	t.Setenv("CATALOG_RECONCILE_INTERVAL", "30s")
	if _, err := boundedDuration("CATALOG_RECONCILE_INTERVAL", time.Minute, 24*time.Hour, time.Hour); err == nil {
		t.Fatal("expected short duration rejection")
	}
}

func TestLoadAppliesPreviewTimeoutDefault(t *testing.T) {
	setRequired(t)

	cfg, err := Load()

	if err != nil {
		t.Fatal(err)
	}
	if cfg.PreviewTimeout != 5*time.Second {
		t.Fatalf("PreviewTimeout = %v, want %v", cfg.PreviewTimeout, 5*time.Second)
	}
}

func TestLoadParsesPreviewTimeout(t *testing.T) {
	setRequired(t)
	t.Setenv("CATALOG_PREVIEW_TIMEOUT", "9s")

	cfg, err := Load()

	if err != nil {
		t.Fatal(err)
	}
	if cfg.PreviewTimeout != 9*time.Second {
		t.Fatalf("PreviewTimeout = %v, want %v", cfg.PreviewTimeout, 9*time.Second)
	}
}

func TestLoadRejectsInvalidPreviewTimeout(t *testing.T) {
	setRequired(t)
	t.Setenv("CATALOG_PREVIEW_TIMEOUT", "500ms")

	if _, err := Load(); err == nil {
		t.Fatal("expected preview timeout rejection")
	}
}
