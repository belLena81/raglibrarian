// Package config loads and validates Catalog runtime configuration.
package config

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
)

// Config is validated Catalog runtime configuration.
type Config struct {
	Address                           string
	DSN                               string
	MinIOEndpoint                     string
	MinIOAccessKey                    string
	MinIOSecretKey                    string
	MinIOBucket                       string
	MinIOInsecure                     bool
	MinIOCAFile                       string
	MinIOCAMaxBytes                   int
	RabbitURI                         string
	IngestionRabbitURI                string
	RetrievalRabbitURI                string
	MaxUploadBytes                    int64
	MaxPreviewBytes                   int
	MaxPreviewPages                   int
	MaxPreviewEPUBEntries             int
	UploadConcurrency                 int
	PreviewConcurrency                int
	PreviewTimeout                    time.Duration
	PersistenceLookupTimeout          time.Duration
	ObjectDeleteTimeout               time.Duration
	OutboxPollInterval                time.Duration
	OutboxDrainBudget                 time.Duration
	OutboxLease                       time.Duration
	OutboxRetryBaseDelay              time.Duration
	OutboxRetryMaxDelay               time.Duration
	OutboxPublishTimeout              time.Duration
	OutboxDialTimeout                 time.Duration
	OutboxHeartbeatTimeout            time.Duration
	DBPingTimeout                     time.Duration
	HealthProbeTimeout                time.Duration
	HealthUpdateInterval              time.Duration
	BacklogPollInterval               time.Duration
	BacklogProbeTimeout               time.Duration
	MetricsReadHeaderTimeout          time.Duration
	MetricsReadTimeout                time.Duration
	MetricsWriteTimeout               time.Duration
	MetricsIdleTimeout                time.Duration
	GRPCGracefulStopTimeout           time.Duration
	MetricsShutdownTimeout            time.Duration
	GRPCReadinessProbeTimeout         time.Duration
	GRPCUploadTimeout                 time.Duration
	GRPCLifecycleTimeout              time.Duration
	GRPCListTimeout                   time.Duration
	ProcessingReconnectInitialBackoff time.Duration
	ProcessingReconnectMaxBackoff     time.Duration
	ProcessingDialTimeout             time.Duration
	ProcessingHeartbeatTimeout        time.Duration
	ProcessingHandleTimeout           time.Duration
	ProcessingRetryLimit              int
	ProcessingRetryDelayStep          time.Duration
	ProcessingRetryPublishTimeout     time.Duration
	MetricsAddress                    string
	ReconcileInterval                 time.Duration
	OrphanGracePeriod                 time.Duration
	TLS                               internaltls.Files
	RunAs                             process.Identity
}

// Load reads Catalog configuration from the environment.
func Load() (Config, error) {
	dsn, err := readSecret("CATALOG_POSTGRES_DSN_FILE", 4096)
	if err != nil {
		return Config{}, err
	}
	minioAccessKey, err := readSecret("CATALOG_MINIO_ACCESS_KEY_FILE", 1024)
	if err != nil {
		return Config{}, err
	}
	minioSecretKey, err := readSecret("CATALOG_MINIO_SECRET_KEY_FILE", 1024)
	if err != nil {
		return Config{}, err
	}
	rabbitURI, err := readSecret("CATALOG_RABBITMQ_URI_FILE", 4096)
	if err != nil {
		return Config{}, err
	}
	ingestionRabbitURI, err := readSecret("CATALOG_INGESTION_RABBITMQ_URI_FILE", 4096)
	if err != nil {
		return Config{}, err
	}
	retrievalRabbitURI, err := readSecret("CATALOG_RETRIEVAL_RABBITMQ_URI_FILE", 4096)
	if err != nil {
		return Config{}, err
	}
	ca, err := required("INTERNAL_TLS_CA_FILE")
	if err != nil {
		return Config{}, err
	}
	cert, err := required("CATALOG_TLS_CERT_FILE")
	if err != nil {
		return Config{}, err
	}
	key, err := required("CATALOG_TLS_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	uid, err := strconv.Atoi(optional("RUN_AS_UID", "65532"))
	if err != nil {
		return Config{}, fmt.Errorf("RUN_AS_UID: %w", err)
	}
	gid, err := strconv.Atoi(optional("RUN_AS_GID", "65532"))
	if err != nil {
		return Config{}, fmt.Errorf("RUN_AS_GID: %w", err)
	}
	if uid < 1 || gid < 1 {
		return Config{}, fmt.Errorf("RUN_AS_UID and RUN_AS_GID must be positive")
	}
	endpoint, err := required("CATALOG_MINIO_ENDPOINT")
	if err != nil {
		return Config{}, err
	}
	if err = validateMinIOEndpoint(endpoint); err != nil {
		return Config{}, err
	}
	minioInsecure, err := strictBool("CATALOG_MINIO_INSECURE", false)
	if err != nil {
		return Config{}, err
	}
	minioCAFile := os.Getenv("CATALOG_MINIO_CA_FILE")
	if minioInsecure && minioCAFile != "" {
		return Config{}, fmt.Errorf("CATALOG_MINIO_CA_FILE cannot be used with insecure object storage")
	}
	bucket, err := required("CATALOG_MINIO_BUCKET")
	if err != nil {
		return Config{}, err
	}
	maxUploadBytes, err := fixedInt64("CATALOG_MAX_UPLOAD_BYTES", 25<<20)
	if err != nil {
		return Config{}, err
	}
	minioCAMaxBytes, err := boundedInt("CATALOG_MINIO_CA_MAX_BYTES", 1<<20, 8<<20)
	if err != nil {
		return Config{}, err
	}
	maxPreviewBytes, err := boundedInt("CATALOG_MAX_PREVIEW_BYTES", 1<<20, 1<<20)
	if err != nil {
		return Config{}, err
	}
	maxPreviewPages, err := boundedInt("CATALOG_MAX_PREVIEW_PAGES", 3, 32)
	if err != nil {
		return Config{}, err
	}
	maxPreviewEPUBEntries, err := boundedInt("CATALOG_MAX_PREVIEW_EPUB_ENTRIES", 2048, 8192)
	if err != nil {
		return Config{}, err
	}
	uploadConcurrency, err := boundedInt("CATALOG_UPLOAD_CONCURRENCY", 2, 16)
	if err != nil {
		return Config{}, err
	}
	previewConcurrency, err := boundedInt("CATALOG_PREVIEW_CONCURRENCY", 2, 16)
	if err != nil {
		return Config{}, err
	}
	previewTimeout, err := boundedDuration("CATALOG_PREVIEW_TIMEOUT", time.Second, 30*time.Second, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	persistenceLookupTimeout, err := boundedDuration("CATALOG_PERSISTENCE_LOOKUP_TIMEOUT", 100*time.Millisecond, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	objectDeleteTimeout, err := boundedDuration("CATALOG_OBJECT_DELETE_TIMEOUT", 100*time.Millisecond, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	outboxPollInterval, err := boundedDuration("CATALOG_OUTBOX_POLL_INTERVAL", 100*time.Millisecond, time.Minute, time.Second)
	if err != nil {
		return Config{}, err
	}
	outboxDrainBudget, err := boundedDuration("CATALOG_OUTBOX_DRAIN_BUDGET", 50*time.Millisecond, 5*time.Second, 250*time.Millisecond)
	if err != nil {
		return Config{}, err
	}
	outboxLease, err := boundedDuration("CATALOG_OUTBOX_LEASE", time.Second, 10*time.Minute, 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	outboxRetryBaseDelay, err := boundedDuration("CATALOG_OUTBOX_RETRY_BASE_DELAY", time.Second, time.Minute, time.Second)
	if err != nil {
		return Config{}, err
	}
	outboxRetryMaxDelay, err := boundedDuration("CATALOG_OUTBOX_RETRY_MAX_DELAY", time.Second, 10*time.Minute, 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	outboxPublishTimeout, err := boundedDuration("CATALOG_OUTBOX_PUBLISH_TIMEOUT", time.Second, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	outboxDialTimeout, err := boundedDuration("CATALOG_OUTBOX_DIAL_TIMEOUT", 100*time.Millisecond, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	outboxHeartbeatTimeout, err := boundedDuration("CATALOG_OUTBOX_HEARTBEAT_TIMEOUT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	dbPingTimeout, err := boundedDuration("CATALOG_DB_PING_TIMEOUT", time.Second, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	healthProbeTimeout, err := boundedDuration("CATALOG_HEALTH_PROBE_TIMEOUT", 100*time.Millisecond, time.Minute, 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	healthUpdateInterval, err := boundedDuration("CATALOG_HEALTH_UPDATE_INTERVAL", 100*time.Millisecond, time.Minute, time.Second)
	if err != nil {
		return Config{}, err
	}
	backlogPollInterval, err := boundedDuration("CATALOG_OUTBOX_BACKLOG_POLL_INTERVAL", time.Second, time.Hour, time.Minute)
	if err != nil {
		return Config{}, err
	}
	backlogProbeTimeout, err := boundedDuration("CATALOG_OUTBOX_BACKLOG_PROBE_TIMEOUT", 100*time.Millisecond, time.Minute, 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	metricsReadHeaderTimeout, err := boundedDuration("CATALOG_METRICS_READ_HEADER_TIMEOUT", time.Second, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	metricsReadTimeout, err := boundedDuration("CATALOG_METRICS_READ_TIMEOUT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	metricsWriteTimeout, err := boundedDuration("CATALOG_METRICS_WRITE_TIMEOUT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	metricsIdleTimeout, err := boundedDuration("CATALOG_METRICS_IDLE_TIMEOUT", time.Second, 5*time.Minute, 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	grpcGracefulStopTimeout, err := boundedDuration("CATALOG_GRPC_GRACEFUL_STOP_TIMEOUT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	metricsShutdownTimeout, err := boundedDuration("CATALOG_METRICS_SHUTDOWN_TIMEOUT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	grpcReadinessProbeTimeout, err := boundedDuration("CATALOG_GRPC_READINESS_PROBE_TIMEOUT", 100*time.Millisecond, time.Minute, 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	grpcUploadTimeout, err := boundedDuration("CATALOG_GRPC_UPLOAD_TIMEOUT", time.Second, 10*time.Minute, 2*time.Minute)
	if err != nil {
		return Config{}, err
	}
	grpcLifecycleTimeout, err := boundedDuration("CATALOG_GRPC_LIFECYCLE_TIMEOUT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	grpcListTimeout, err := boundedDuration("CATALOG_GRPC_LIST_TIMEOUT", 100*time.Millisecond, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	processingReconnectInitialBackoff, err := boundedDuration("CATALOG_PROCESSING_RECONNECT_INITIAL_BACKOFF", 100*time.Millisecond, time.Minute, time.Second)
	if err != nil {
		return Config{}, err
	}
	processingReconnectMaxBackoff, err := boundedDuration("CATALOG_PROCESSING_RECONNECT_MAX_BACKOFF", time.Second, 10*time.Minute, 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	processingDialTimeout, err := boundedDuration("CATALOG_PROCESSING_DIAL_TIMEOUT", 100*time.Millisecond, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	processingHeartbeatTimeout, err := boundedDuration("CATALOG_PROCESSING_HEARTBEAT_TIMEOUT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	processingHandleTimeout, err := boundedDuration("CATALOG_PROCESSING_HANDLE_TIMEOUT", 100*time.Millisecond, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	processingRetryLimit, err := boundedInt("CATALOG_PROCESSING_RETRY_LIMIT", 5, 32)
	if err != nil {
		return Config{}, err
	}
	processingRetryDelayStep, err := boundedDuration("CATALOG_PROCESSING_RETRY_DELAY_STEP", 10*time.Millisecond, 10*time.Second, 250*time.Millisecond)
	if err != nil {
		return Config{}, err
	}
	processingRetryPublishTimeout, err := boundedDuration("CATALOG_PROCESSING_RETRY_PUBLISH_TIMEOUT", 100*time.Millisecond, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	metricsAddress, err := privateMetricsAddress(optional("CATALOG_METRICS_ADDR", "127.0.0.1:9092"))
	if err != nil {
		return Config{}, err
	}
	reconcileInterval, err := boundedDuration("CATALOG_RECONCILE_INTERVAL", time.Minute, 24*time.Hour, 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	orphanGracePeriod, err := boundedDuration("CATALOG_ORPHAN_GRACE_PERIOD", 5*time.Minute, 7*24*time.Hour, time.Hour)
	if err != nil {
		return Config{}, err
	}
	return Config{Address: optional("CATALOG_GRPC_ADDR", ":50052"), DSN: dsn, MinIOEndpoint: endpoint, MinIOAccessKey: minioAccessKey, MinIOSecretKey: minioSecretKey, MinIOBucket: bucket, MinIOInsecure: minioInsecure, MinIOCAFile: minioCAFile, MinIOCAMaxBytes: minioCAMaxBytes, RabbitURI: rabbitURI, IngestionRabbitURI: ingestionRabbitURI, RetrievalRabbitURI: retrievalRabbitURI, MaxUploadBytes: maxUploadBytes, MaxPreviewBytes: maxPreviewBytes, MaxPreviewPages: maxPreviewPages, MaxPreviewEPUBEntries: maxPreviewEPUBEntries, UploadConcurrency: uploadConcurrency, PreviewConcurrency: previewConcurrency, PreviewTimeout: previewTimeout, PersistenceLookupTimeout: persistenceLookupTimeout, ObjectDeleteTimeout: objectDeleteTimeout, OutboxPollInterval: outboxPollInterval, OutboxDrainBudget: outboxDrainBudget, OutboxLease: outboxLease, OutboxRetryBaseDelay: outboxRetryBaseDelay, OutboxRetryMaxDelay: outboxRetryMaxDelay, OutboxPublishTimeout: outboxPublishTimeout, OutboxDialTimeout: outboxDialTimeout, OutboxHeartbeatTimeout: outboxHeartbeatTimeout, DBPingTimeout: dbPingTimeout, HealthProbeTimeout: healthProbeTimeout, HealthUpdateInterval: healthUpdateInterval, BacklogPollInterval: backlogPollInterval, BacklogProbeTimeout: backlogProbeTimeout, MetricsReadHeaderTimeout: metricsReadHeaderTimeout, MetricsReadTimeout: metricsReadTimeout, MetricsWriteTimeout: metricsWriteTimeout, MetricsIdleTimeout: metricsIdleTimeout, GRPCGracefulStopTimeout: grpcGracefulStopTimeout, MetricsShutdownTimeout: metricsShutdownTimeout, GRPCReadinessProbeTimeout: grpcReadinessProbeTimeout, GRPCUploadTimeout: grpcUploadTimeout, GRPCLifecycleTimeout: grpcLifecycleTimeout, GRPCListTimeout: grpcListTimeout, ProcessingReconnectInitialBackoff: processingReconnectInitialBackoff, ProcessingReconnectMaxBackoff: processingReconnectMaxBackoff, ProcessingDialTimeout: processingDialTimeout, ProcessingHeartbeatTimeout: processingHeartbeatTimeout, ProcessingHandleTimeout: processingHandleTimeout, ProcessingRetryLimit: processingRetryLimit, ProcessingRetryDelayStep: processingRetryDelayStep, ProcessingRetryPublishTimeout: processingRetryPublishTimeout, MetricsAddress: metricsAddress, ReconcileInterval: reconcileInterval, OrphanGracePeriod: orphanGracePeriod, TLS: internaltls.Files{CA: ca, Certificate: cert, Key: key}, RunAs: process.Identity{UID: uid, GID: gid}}, nil
}

func strictBool(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	if value != "true" && value != "false" {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return value == "true", nil
}

func boundedDuration(key string, minimum, maximum, fallback time.Duration) (time.Duration, error) {
	value, err := time.ParseDuration(optional(key, fallback.String()))
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %s and %s", key, minimum, maximum)
	}
	return value, nil
}

func validateMinIOEndpoint(endpoint string) error {
	if endpoint == "" || strings.Contains(endpoint, "://") || strings.ContainsAny(endpoint, "/?#@") {
		return fmt.Errorf("CATALOG_MINIO_ENDPOINT must contain only a host and optional port")
	}
	parsed, err := url.Parse("https://" + endpoint)
	if err != nil || parsed.Host != endpoint || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" {
		return fmt.Errorf("CATALOG_MINIO_ENDPOINT must contain only a host and optional port")
	}
	return nil
}

func fixedInt64(key string, supported int64) (int64, error) {
	value, err := strconv.ParseInt(optional(key, strconv.FormatInt(supported, 10)), 10, 64)
	if err != nil || value != supported {
		return 0, fmt.Errorf("%s must be %d for the supported processing profile", key, supported)
	}
	return value, nil
}

func boundedInt(key string, fallback, maximum int) (int, error) {
	value, err := strconv.Atoi(optional(key, strconv.Itoa(fallback)))
	if err != nil || value < 1 || value > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, maximum)
	}
	return value, nil
}

func privateMetricsAddress(value string) (string, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return "", fmt.Errorf("CATALOG_METRICS_ADDR is invalid")
	}
	if host == "localhost" {
		return value, nil
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsPrivate()) {
		return "", fmt.Errorf("CATALOG_METRICS_ADDR must use a private address")
	}
	return value, nil
}

func readSecret(key string, maxSize int) (string, error) {
	path, err := required(key)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path) // #nosec G304 -- operator-provided secret-file setting.
	if err != nil {
		return "", fmt.Errorf("%s is invalid", key)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > int64(maxSize) {
		return "", fmt.Errorf("%s is invalid", key)
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(maxSize)+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || len(contents) > maxSize || value == "" {
		return "", fmt.Errorf("%s is invalid", key)
	}
	return value, nil
}

func required(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}
func optional(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
