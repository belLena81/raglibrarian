// Package config loads and validates Ingestion runtime configuration.
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

	"github.com/belLena81/raglibrarian/pkg/process"
)

type Config struct {
	DSN, RabbitURI, MinIOEndpoint, MinIOAccessKey, MinIOSecretKey string
	SourceBucket, ArtifactBucket, MinIOCAFile, MetricsAddress     string
	TokenizerFile, PDFInfoPath, PDFTextPath, TemporaryDirectory   string
	Queue, ResultExchange                                         string
	MinIOInsecure                                                 bool
	WorkConcurrency, MaximumAttempts, MaximumChunks               int
	MaximumSourceBytes, MaximumExtractedBytes, MaximumPageBytes   int64
	MaximumManifestBytes, MaximumTemporaryBytes                   int64
	MaximumPages                                                  uint32
	ProcessingTimeout, JobLease, OutboxInterval                   time.Duration
	CleanupInterval, OrphanGracePeriod                            time.Duration
	RunAs                                                         process.Identity
}

// CleanupConfig deliberately contains no source-store, RabbitMQ, parser or
// tokenizer settings. Deployments should back it with cleanup-only database
// and artifact-store credentials.
type CleanupConfig struct {
	DSN, MinIOEndpoint, MinIOAccessKey, MinIOSecretKey string
	ArtifactBucket, MinIOCAFile                        string
	MinIOInsecure                                      bool
	CleanupInterval, OrphanGracePeriod                 time.Duration
}

func LoadCleanup() (CleanupConfig, error) {
	dsn, err := readSecret("INGESTION_CLEANUP_POSTGRES_DSN_FILE", 4096)
	if err != nil {
		return CleanupConfig{}, err
	}
	accessKey, err := readSecret("INGESTION_CLEANUP_MINIO_ACCESS_KEY_FILE", 1024)
	if err != nil {
		return CleanupConfig{}, err
	}
	secretKey, err := readSecret("INGESTION_CLEANUP_MINIO_SECRET_KEY_FILE", 1024)
	if err != nil {
		return CleanupConfig{}, err
	}
	endpoint, err := required("INGESTION_MINIO_ENDPOINT")
	if err != nil {
		return CleanupConfig{}, err
	}
	if err = validateEndpoint(endpoint); err != nil {
		return CleanupConfig{}, err
	}
	insecure, err := strictBool("INGESTION_MINIO_INSECURE", false)
	if err != nil {
		return CleanupConfig{}, err
	}
	caFile := os.Getenv("INGESTION_MINIO_CA_FILE")
	if insecure && caFile != "" {
		return CleanupConfig{}, fmt.Errorf("INGESTION_MINIO_CA_FILE cannot be used with insecure object storage")
	}
	artifactBucket, err := required("INGESTION_ARTIFACT_BUCKET")
	if err != nil {
		return CleanupConfig{}, err
	}
	cleanupInterval, err := boundedDuration("INGESTION_CLEANUP_INTERVAL", time.Minute, 24*time.Hour, 15*time.Minute)
	if err != nil {
		return CleanupConfig{}, err
	}
	orphanGracePeriod, err := boundedDuration("INGESTION_ORPHAN_GRACE_PERIOD", 15*time.Minute, 7*24*time.Hour, time.Hour)
	if err != nil {
		return CleanupConfig{}, err
	}
	return CleanupConfig{
		DSN:               dsn,
		MinIOEndpoint:     endpoint,
		MinIOAccessKey:    accessKey,
		MinIOSecretKey:    secretKey,
		ArtifactBucket:    artifactBucket,
		MinIOCAFile:       caFile,
		MinIOInsecure:     insecure,
		CleanupInterval:   cleanupInterval,
		OrphanGracePeriod: orphanGracePeriod,
	}, nil
}

func Load() (Config, error) {
	dsn, err := readSecret("INGESTION_POSTGRES_DSN_FILE", 4096)
	if err != nil {
		return Config{}, err
	}
	rabbitURI, err := readSecret("INGESTION_RABBITMQ_URI_FILE", 4096)
	if err != nil {
		return Config{}, err
	}
	accessKey, err := readSecret("INGESTION_MINIO_ACCESS_KEY_FILE", 1024)
	if err != nil {
		return Config{}, err
	}
	secretKey, err := readSecret("INGESTION_MINIO_SECRET_KEY_FILE", 1024)
	if err != nil {
		return Config{}, err
	}
	endpoint, err := required("INGESTION_MINIO_ENDPOINT")
	if err != nil {
		return Config{}, err
	}
	if err = validateEndpoint(endpoint); err != nil {
		return Config{}, err
	}
	insecure, err := strictBool("INGESTION_MINIO_INSECURE", false)
	if err != nil {
		return Config{}, err
	}
	caFile := os.Getenv("INGESTION_MINIO_CA_FILE")
	if insecure && caFile != "" {
		return Config{}, fmt.Errorf("INGESTION_MINIO_CA_FILE cannot be used with insecure object storage")
	}
	sourceBucket, err := required("INGESTION_SOURCE_BUCKET")
	if err != nil {
		return Config{}, err
	}
	artifactBucket, err := required("INGESTION_ARTIFACT_BUCKET")
	if err != nil {
		return Config{}, err
	}
	if sourceBucket == artifactBucket {
		return Config{}, fmt.Errorf("source and artifact buckets must differ")
	}
	tokenizerFile, err := required("INGESTION_TOKENIZER_FILE")
	if err != nil {
		return Config{}, err
	}
	metrics, err := privateAddress(optional("INGESTION_METRICS_ADDR", "127.0.0.1:9093"))
	if err != nil {
		return Config{}, err
	}
	workConcurrency, err := boundedInt("INGESTION_WORK_CONCURRENCY", 2, 16)
	if err != nil {
		return Config{}, err
	}
	maximumAttempts, err := boundedInt("INGESTION_MAX_ATTEMPTS", 4, 10)
	if err != nil {
		return Config{}, err
	}
	maximumChunks, err := boundedInt("INGESTION_MAX_CHUNKS", 50000, 100000)
	if err != nil {
		return Config{}, err
	}
	maximumPages64, err := boundedInt64("INGESTION_MAX_PAGES", 1000, 10000)
	if err != nil {
		return Config{}, err
	}
	maximumSource, err := boundedInt64("INGESTION_MAX_SOURCE_BYTES", 50<<20, 512<<20)
	if err != nil {
		return Config{}, err
	}
	maximumExtracted, err := boundedInt64("INGESTION_MAX_EXTRACTED_BYTES", 128<<20, 1<<30)
	if err != nil {
		return Config{}, err
	}
	maximumPage, err := boundedInt64("INGESTION_MAX_PAGE_BYTES", 2<<20, 32<<20)
	if err != nil {
		return Config{}, err
	}
	maximumManifest, err := boundedInt64("INGESTION_MAX_MANIFEST_BYTES", 1<<20, 16<<20)
	if err != nil {
		return Config{}, err
	}
	maximumTemporary, err := boundedInt64("INGESTION_MAX_TEMP_BYTES", 1<<30, 10<<30)
	if err != nil {
		return Config{}, err
	}
	if maximumTemporary < maximumSource {
		return Config{}, fmt.Errorf("INGESTION_MAX_TEMP_BYTES must be at least INGESTION_MAX_SOURCE_BYTES")
	}
	timeout, err := boundedDuration("INGESTION_PROCESSING_TIMEOUT", time.Minute, 13*time.Minute+30*time.Second, 12*time.Minute+30*time.Second)
	if err != nil {
		return Config{}, err
	}
	lease, err := boundedDuration("INGESTION_JOB_LEASE", timeout, 30*time.Minute, 13*time.Minute)
	if err != nil {
		return Config{}, err
	}
	if lease < timeout+30*time.Second {
		return Config{}, fmt.Errorf("INGESTION_JOB_LEASE must exceed INGESTION_PROCESSING_TIMEOUT by at least 30s")
	}
	outboxInterval, err := boundedDuration("INGESTION_OUTBOX_INTERVAL", 100*time.Millisecond, time.Minute, time.Second)
	if err != nil {
		return Config{}, err
	}
	cleanupInterval, err := boundedDuration("INGESTION_CLEANUP_INTERVAL", time.Minute, 24*time.Hour, 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	orphanGracePeriod, err := boundedDuration("INGESTION_ORPHAN_GRACE_PERIOD", 15*time.Minute, 7*24*time.Hour, time.Hour)
	if err != nil {
		return Config{}, err
	}
	uid, err := boundedInt("RUN_AS_UID", 65532, 1<<30)
	if err != nil {
		return Config{}, err
	}
	gid, err := boundedInt("RUN_AS_GID", 65532, 1<<30)
	if err != nil {
		return Config{}, err
	}
	maximumPages := uint32(maximumPages64) // #nosec G115 -- bounded above to 10,000.
	return Config{
		DSN:                   dsn,
		RabbitURI:             rabbitURI,
		MinIOEndpoint:         endpoint,
		MinIOAccessKey:        accessKey,
		MinIOSecretKey:        secretKey,
		SourceBucket:          sourceBucket,
		ArtifactBucket:        artifactBucket,
		MinIOCAFile:           caFile,
		MetricsAddress:        metrics,
		TokenizerFile:         tokenizerFile,
		PDFInfoPath:           optional("INGESTION_PDFINFO_PATH", "/usr/bin/pdfinfo"),
		PDFTextPath:           optional("INGESTION_PDFTOTEXT_PATH", "/usr/bin/pdftotext"),
		TemporaryDirectory:    optional("INGESTION_TEMP_DIR", "/tmp"),
		Queue:                 optional("INGESTION_QUEUE", "ingestion.book-uploaded.v1"),
		ResultExchange:        optional("INGESTION_RESULT_EXCHANGE", "raglibrarian.ingestion.events.v1"),
		MinIOInsecure:         insecure,
		WorkConcurrency:       workConcurrency,
		MaximumAttempts:       maximumAttempts,
		MaximumChunks:         maximumChunks,
		MaximumSourceBytes:    maximumSource,
		MaximumExtractedBytes: maximumExtracted,
		MaximumPageBytes:      maximumPage,
		MaximumManifestBytes:  maximumManifest,
		MaximumTemporaryBytes: maximumTemporary,
		MaximumPages:          maximumPages,
		ProcessingTimeout:     timeout,
		JobLease:              lease,
		OutboxInterval:        outboxInterval,
		CleanupInterval:       cleanupInterval,
		OrphanGracePeriod:     orphanGracePeriod,
		RunAs:                 process.Identity{UID: uid, GID: gid},
	}, nil
}

func readSecret(key string, maximum int) (string, error) {
	path, err := required(key)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path) // #nosec G304 -- operator-provided secret path.
	if err != nil {
		return "", fmt.Errorf("%s is invalid", key)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > int64(maximum) {
		return "", fmt.Errorf("%s is invalid", key)
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || len(contents) > maximum || value == "" {
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
func strictBool(key string, fallback bool) (bool, error) {
	value := optional(key, strconv.FormatBool(fallback))
	if value != "true" && value != "false" {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return value == "true", nil
}
func boundedInt(key string, fallback, maximum int) (int, error) {
	value, err := strconv.Atoi(optional(key, strconv.Itoa(fallback)))
	if err != nil || value < 1 || value > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, maximum)
	}
	return value, nil
}
func boundedInt64(key string, fallback, maximum int64) (int64, error) {
	value, err := strconv.ParseInt(optional(key, strconv.FormatInt(fallback, 10)), 10, 64)
	if err != nil || value < 1 || value > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, maximum)
	}
	return value, nil
}
func boundedDuration(key string, minimum, maximum, fallback time.Duration) (time.Duration, error) {
	value, err := time.ParseDuration(optional(key, fallback.String()))
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %s and %s", key, minimum, maximum)
	}
	return value, nil
}
func validateEndpoint(endpoint string) error {
	if strings.Contains(endpoint, "://") || strings.ContainsAny(endpoint, "/?#@") {
		return fmt.Errorf("INGESTION_MINIO_ENDPOINT must contain host and optional port")
	}
	parsed, err := url.Parse("https://" + endpoint)
	if err != nil || parsed.Hostname() == "" || parsed.Host != endpoint {
		return fmt.Errorf("INGESTION_MINIO_ENDPOINT is invalid")
	}
	return nil
}
func privateAddress(value string) (string, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return "", fmt.Errorf("INGESTION_METRICS_ADDR is invalid")
	}
	if host == "localhost" {
		return value, nil
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified()) {
		return "", fmt.Errorf("INGESTION_METRICS_ADDR must be private")
	}
	return value, nil
}
