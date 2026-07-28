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
	if cfg.PersistenceLookupTimeout != 5*time.Second || cfg.ObjectDeleteTimeout != 5*time.Second {
		t.Fatalf("unexpected catalog service timeouts: %#v", cfg)
	}
}

func TestLoadParsesPreviewTimeout(t *testing.T) {
	setRequired(t)
	t.Setenv("CATALOG_PREVIEW_TIMEOUT", "9s")
	t.Setenv("CATALOG_PERSISTENCE_LOOKUP_TIMEOUT", "6s")
	t.Setenv("CATALOG_OBJECT_DELETE_TIMEOUT", "7s")

	cfg, err := Load()

	if err != nil {
		t.Fatal(err)
	}
	if cfg.PreviewTimeout != 9*time.Second {
		t.Fatalf("PreviewTimeout = %v, want %v", cfg.PreviewTimeout, 9*time.Second)
	}
	if cfg.PersistenceLookupTimeout != 6*time.Second || cfg.ObjectDeleteTimeout != 7*time.Second {
		t.Fatalf("unexpected catalog service timeouts: %#v", cfg)
	}
}

func TestLoadAppliesOutboxPolicyDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutboxPollInterval != time.Second || cfg.OutboxDrainBudget != 250*time.Millisecond ||
		cfg.OutboxLease != 30*time.Second || cfg.OutboxRetryBaseDelay != time.Second || cfg.OutboxRetryMaxDelay != 5*time.Minute || cfg.OutboxPublishTimeout != 5*time.Second {
		t.Fatalf("unexpected outbox policy: %#v", cfg)
	}
}

func TestLoadParsesOutboxPolicy(t *testing.T) {
	setRequired(t)
	t.Setenv("CATALOG_OUTBOX_POLL_INTERVAL", "2s")
	t.Setenv("CATALOG_OUTBOX_DRAIN_BUDGET", "500ms")
	t.Setenv("CATALOG_OUTBOX_LEASE", "45s")
	t.Setenv("CATALOG_OUTBOX_RETRY_BASE_DELAY", "2s")
	t.Setenv("CATALOG_OUTBOX_RETRY_MAX_DELAY", "6m")
	t.Setenv("CATALOG_OUTBOX_PUBLISH_TIMEOUT", "8s")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutboxPollInterval != 2*time.Second || cfg.OutboxDrainBudget != 500*time.Millisecond ||
		cfg.OutboxLease != 45*time.Second || cfg.OutboxRetryBaseDelay != 2*time.Second || cfg.OutboxRetryMaxDelay != 6*time.Minute || cfg.OutboxPublishTimeout != 8*time.Second {
		t.Fatalf("unexpected outbox policy: %#v", cfg)
	}
}

func TestLoadAppliesAppPolicyDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBPingTimeout != 5*time.Second || cfg.HealthProbeTimeout != 2*time.Second || cfg.HealthUpdateInterval != time.Second ||
		cfg.BacklogPollInterval != time.Minute || cfg.BacklogProbeTimeout != 2*time.Second ||
		cfg.MetricsReadHeaderTimeout != 5*time.Second || cfg.MetricsReadTimeout != 10*time.Second ||
		cfg.MetricsWriteTimeout != 10*time.Second || cfg.MetricsIdleTimeout != 30*time.Second ||
		cfg.GRPCGracefulStopTimeout != 10*time.Second || cfg.MetricsShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected app policy defaults: %#v", cfg)
	}
}

func TestLoadParsesAppPolicy(t *testing.T) {
	setRequired(t)
	t.Setenv("CATALOG_DB_PING_TIMEOUT", "7s")
	t.Setenv("CATALOG_HEALTH_PROBE_TIMEOUT", "3s")
	t.Setenv("CATALOG_HEALTH_UPDATE_INTERVAL", "2s")
	t.Setenv("CATALOG_OUTBOX_BACKLOG_POLL_INTERVAL", "2m")
	t.Setenv("CATALOG_OUTBOX_BACKLOG_PROBE_TIMEOUT", "4s")
	t.Setenv("CATALOG_METRICS_READ_HEADER_TIMEOUT", "6s")
	t.Setenv("CATALOG_METRICS_READ_TIMEOUT", "11s")
	t.Setenv("CATALOG_METRICS_WRITE_TIMEOUT", "12s")
	t.Setenv("CATALOG_METRICS_IDLE_TIMEOUT", "31s")
	t.Setenv("CATALOG_GRPC_GRACEFUL_STOP_TIMEOUT", "9s")
	t.Setenv("CATALOG_METRICS_SHUTDOWN_TIMEOUT", "8s")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBPingTimeout != 7*time.Second || cfg.HealthProbeTimeout != 3*time.Second || cfg.HealthUpdateInterval != 2*time.Second ||
		cfg.BacklogPollInterval != 2*time.Minute || cfg.BacklogProbeTimeout != 4*time.Second ||
		cfg.MetricsReadHeaderTimeout != 6*time.Second || cfg.MetricsReadTimeout != 11*time.Second ||
		cfg.MetricsWriteTimeout != 12*time.Second || cfg.MetricsIdleTimeout != 31*time.Second ||
		cfg.GRPCGracefulStopTimeout != 9*time.Second || cfg.MetricsShutdownTimeout != 8*time.Second {
		t.Fatalf("unexpected app policy: %#v", cfg)
	}
}

func TestLoadAppliesGRPCAndProcessingPolicyDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPCReadinessProbeTimeout != 2*time.Second || cfg.GRPCUploadTimeout != 2*time.Minute ||
		cfg.GRPCLifecycleTimeout != 10*time.Second || cfg.GRPCListTimeout != 5*time.Second ||
		cfg.ProcessingReconnectInitialBackoff != time.Second || cfg.ProcessingReconnectMaxBackoff != 30*time.Second ||
		cfg.ProcessingDialTimeout != 5*time.Second || cfg.ProcessingHeartbeatTimeout != 10*time.Second ||
		cfg.ProcessingHandleTimeout != 10*time.Second || cfg.ProcessingRetryLimit != 5 ||
		cfg.ProcessingRetryDelayStep != 250*time.Millisecond || cfg.ProcessingRetryPublishTimeout != 5*time.Second {
		t.Fatalf("unexpected grpc/processing policy defaults: %#v", cfg)
	}
}

func TestLoadParsesGRPCAndProcessingPolicy(t *testing.T) {
	setRequired(t)
	t.Setenv("CATALOG_GRPC_READINESS_PROBE_TIMEOUT", "3s")
	t.Setenv("CATALOG_GRPC_UPLOAD_TIMEOUT", "3m")
	t.Setenv("CATALOG_GRPC_LIFECYCLE_TIMEOUT", "12s")
	t.Setenv("CATALOG_GRPC_LIST_TIMEOUT", "6s")
	t.Setenv("CATALOG_PROCESSING_RECONNECT_INITIAL_BACKOFF", "2s")
	t.Setenv("CATALOG_PROCESSING_RECONNECT_MAX_BACKOFF", "40s")
	t.Setenv("CATALOG_PROCESSING_DIAL_TIMEOUT", "6s")
	t.Setenv("CATALOG_PROCESSING_HEARTBEAT_TIMEOUT", "12s")
	t.Setenv("CATALOG_PROCESSING_HANDLE_TIMEOUT", "11s")
	t.Setenv("CATALOG_PROCESSING_RETRY_LIMIT", "6")
	t.Setenv("CATALOG_PROCESSING_RETRY_DELAY_STEP", "300ms")
	t.Setenv("CATALOG_PROCESSING_RETRY_PUBLISH_TIMEOUT", "7s")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPCReadinessProbeTimeout != 3*time.Second || cfg.GRPCUploadTimeout != 3*time.Minute ||
		cfg.GRPCLifecycleTimeout != 12*time.Second || cfg.GRPCListTimeout != 6*time.Second ||
		cfg.ProcessingReconnectInitialBackoff != 2*time.Second || cfg.ProcessingReconnectMaxBackoff != 40*time.Second ||
		cfg.ProcessingDialTimeout != 6*time.Second || cfg.ProcessingHeartbeatTimeout != 12*time.Second ||
		cfg.ProcessingHandleTimeout != 11*time.Second || cfg.ProcessingRetryLimit != 6 ||
		cfg.ProcessingRetryDelayStep != 300*time.Millisecond || cfg.ProcessingRetryPublishTimeout != 7*time.Second {
		t.Fatalf("unexpected grpc/processing policy: %#v", cfg)
	}
}

func TestLoadRejectsInvalidPreviewTimeout(t *testing.T) {
	setRequired(t)
	t.Setenv("CATALOG_PREVIEW_TIMEOUT", "500ms")

	if _, err := Load(); err == nil {
		t.Fatal("expected preview timeout rejection")
	}
}
