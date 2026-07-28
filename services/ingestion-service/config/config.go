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
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
)

const (
	defaultParserSandboxMemoryBytes = int64(1536 << 20)
	maximumParserSandboxMemoryBytes = int64(8 << 30)
)

type Config struct {
	RuntimeBackend                                                              string
	DSN, RabbitURI, MinIOEndpoint, MinIOAccessKey, MinIOSecretKey               string
	SourceBucket, ArtifactBucket, MinIOCAFile, MetricsAddress                   string
	AWSRegion, KMSKeyARN                                                        string
	TokenizerFile, PDFInfoPath, PDFTextPath, EPUBParserPath, TemporaryDirectory string
	DebugDumpPDFTextDirectory                                                   string
	Queue, ResultExchange                                                       string
	MinIOInsecure                                                               bool
	WorkConcurrency, MaximumAttempts, MaximumChunks                             int
	ChunkMaximumTokens, ChunkOverlapTokens, ChunkTargetPages, ChunkMaximumPages int
	MaximumSourceBytes, MaximumExtractedBytes, MaximumPageBytes                 int64
	MaximumManifestBytes, MaximumTemporaryBytes                                 int64
	ArtifactChunksPerShard                                                      int
	ArtifactVersionCleanupPasses                                                int
	ArtifactMaximumShardBytes                                                   int64
	MemoryLimitBytes, ParserSandboxMemoryBytes, ParserRuntimeHeadroomBytes      int64
	MaximumPages                                                                uint32
	ProcessingTimeout, PersistenceTimeout, ArtifactAbortTimeout, JobLease       time.Duration
	FirstRetryDelay, SecondRetryDelay, SubsequentRetryDelay, OutboxInterval     time.Duration
	RabbitDialTimeout, RabbitHeartbeat, RabbitPublishTimeout, OutboxLease       time.Duration
	RetryDispatchDelay, OutboxRetryBaseDelay, OutboxRetryMaxDelay               time.Duration
	CleanupInterval, OrphanGracePeriod                                          time.Duration
	WorkerReadinessProbeTimeout, WorkerReadinessRefreshInterval                 time.Duration
	WorkerMetricsReadHeaderTimeout, WorkerMetricsShutdownTimeout                time.Duration
	RunAs                                                                       process.Identity
}

// CleanupConfig deliberately contains no source-store, RabbitMQ, parser or
// tokenizer settings. Deployments should back it with cleanup-only database
// and artifact-store credentials.
type CleanupConfig struct {
	RuntimeBackend                                     string
	DSN, MinIOEndpoint, MinIOAccessKey, MinIOSecretKey string
	ArtifactBucket, MinIOCAFile                        string
	AWSRegion, KMSKeyARN                               string
	MinIOInsecure                                      bool
	ArtifactVersionCleanupPasses                       int
	RetryDispatchDelay, OutboxRetryBaseDelay           time.Duration
	OutboxRetryMaxDelay                                time.Duration
	CleanupInterval, OrphanGracePeriod                 time.Duration
}

// DispatcherConfig contains only the settings used to publish ingestion
// outbox records. It intentionally excludes worker object-store and parser
// credentials.
type DispatcherConfig struct {
	RuntimeBackend string
	DSN            string
	RabbitURI      string
	ResultExchange string
	OutboxInterval time.Duration
	RabbitDialTimeout,
	RabbitHeartbeat,
	RabbitPublishTimeout,
	OutboxLease,
	RetryDispatchDelay,
	OutboxRetryBaseDelay,
	OutboxRetryMaxDelay time.Duration
	RunAs process.Identity
}

func loadLocalDispatcher() (DispatcherConfig, error) {
	dsn, err := readSecret("INGESTION_POSTGRES_DSN_FILE", 4096)
	if err != nil {
		return DispatcherConfig{}, err
	}
	rabbitURI, err := readSecret("INGESTION_RABBITMQ_URI_FILE", 4096)
	if err != nil {
		return DispatcherConfig{}, err
	}
	outboxInterval, err := boundedDuration("INGESTION_OUTBOX_INTERVAL", 100*time.Millisecond, time.Minute, time.Second)
	if err != nil {
		return DispatcherConfig{}, err
	}
	rabbitDialTimeout, err := boundedDuration("INGESTION_RABBITMQ_DIAL_TIMEOUT", time.Second, time.Minute, 5*time.Second)
	if err != nil {
		return DispatcherConfig{}, err
	}
	rabbitHeartbeat, err := boundedDuration("INGESTION_RABBITMQ_HEARTBEAT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return DispatcherConfig{}, err
	}
	rabbitPublishTimeout, err := boundedDuration("INGESTION_RABBITMQ_PUBLISH_TIMEOUT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return DispatcherConfig{}, err
	}
	outboxLease, err := boundedDuration("INGESTION_OUTBOX_LEASE", time.Second, 5*time.Minute, 30*time.Second)
	if err != nil {
		return DispatcherConfig{}, err
	}
	retryDispatchDelay, err := boundedDuration("INGESTION_RETRY_DISPATCH_DELAY", time.Second, time.Minute, time.Second)
	if err != nil {
		return DispatcherConfig{}, err
	}
	outboxRetryBaseDelay, err := boundedDuration("INGESTION_OUTBOX_RETRY_BASE_DELAY", time.Second, time.Minute, time.Second)
	if err != nil {
		return DispatcherConfig{}, err
	}
	outboxRetryMaxDelay, err := boundedDuration("INGESTION_OUTBOX_RETRY_MAX_DELAY", time.Second, 10*time.Minute, 5*time.Minute)
	if err != nil {
		return DispatcherConfig{}, err
	}
	uid, err := boundedInt("RUN_AS_UID", 65532, 1<<30)
	if err != nil {
		return DispatcherConfig{}, err
	}
	gid, err := boundedInt("RUN_AS_GID", 65532, 1<<30)
	if err != nil {
		return DispatcherConfig{}, err
	}
	return DispatcherConfig{
		RuntimeBackend:       "local",
		DSN:                  dsn,
		RabbitURI:            rabbitURI,
		ResultExchange:       optional("INGESTION_RESULT_EXCHANGE", "raglibrarian.ingestion.events.v1"),
		OutboxInterval:       outboxInterval,
		RabbitDialTimeout:    rabbitDialTimeout,
		RabbitHeartbeat:      rabbitHeartbeat,
		RabbitPublishTimeout: rabbitPublishTimeout,
		OutboxLease:          outboxLease,
		RetryDispatchDelay:   retryDispatchDelay,
		OutboxRetryBaseDelay: outboxRetryBaseDelay,
		OutboxRetryMaxDelay:  outboxRetryMaxDelay,
		RunAs:                process.Identity{UID: uid, GID: gid},
	}, nil
}

func loadLocalCleanup() (CleanupConfig, error) {
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
	artifactVersionCleanupPasses, err := boundedInt("INGESTION_ARTIFACT_VERSION_CLEANUP_PASSES", 256, 4096)
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
	retryDispatchDelay, err := boundedDuration("INGESTION_RETRY_DISPATCH_DELAY", time.Second, time.Minute, time.Second)
	if err != nil {
		return CleanupConfig{}, err
	}
	outboxRetryBaseDelay, err := boundedDuration("INGESTION_OUTBOX_RETRY_BASE_DELAY", time.Second, time.Minute, time.Second)
	if err != nil {
		return CleanupConfig{}, err
	}
	outboxRetryMaxDelay, err := boundedDuration("INGESTION_OUTBOX_RETRY_MAX_DELAY", time.Second, 10*time.Minute, 5*time.Minute)
	if err != nil {
		return CleanupConfig{}, err
	}
	return CleanupConfig{
		RuntimeBackend:               "local",
		DSN:                          dsn,
		MinIOEndpoint:                endpoint,
		MinIOAccessKey:               accessKey,
		MinIOSecretKey:               secretKey,
		ArtifactBucket:               artifactBucket,
		ArtifactVersionCleanupPasses: artifactVersionCleanupPasses,
		MinIOCAFile:                  caFile,
		MinIOInsecure:                insecure,
		RetryDispatchDelay:           retryDispatchDelay,
		OutboxRetryBaseDelay:         outboxRetryBaseDelay,
		OutboxRetryMaxDelay:          outboxRetryMaxDelay,
		CleanupInterval:              cleanupInterval,
		OrphanGracePeriod:            orphanGracePeriod,
	}, nil
}

func loadLocal() (Config, error) {
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
	// One parser uses a configurable 1536 MiB default address-space budget.
	// Horizontal worker scaling provides concurrency without overcommitting the
	// worker memory envelope.
	workConcurrency, err := boundedInt("INGESTION_WORK_CONCURRENCY", 1, 16)
	if err != nil {
		return Config{}, err
	}
	maximumAttempts, err := boundedInt("INGESTION_MAX_ATTEMPTS", 4, 10)
	if err != nil {
		return Config{}, err
	}
	maximumChunks, err := fixedInt("INGESTION_MAX_CHUNKS", 50_000)
	if err != nil {
		return Config{}, err
	}
	chunkMaximumTokens, chunkOverlapTokens, chunkTargetPages, chunkMaximumPages, err := chunkPolicyValues()
	if err != nil {
		return Config{}, err
	}
	maximumPages64, err := boundedInt64("INGESTION_MAX_PAGES", 1000, 1000)
	if err != nil {
		return Config{}, err
	}
	maximumSource, err := fixedInt64("INGESTION_MAX_SOURCE_BYTES", 25<<20)
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
	maximumManifest, err := boundedInt64("INGESTION_MAX_MANIFEST_BYTES", 1<<20, 1<<20)
	if err != nil {
		return Config{}, err
	}
	artifactChunksPerShard, err := boundedInt("INGESTION_ARTIFACT_CHUNKS_PER_SHARD", 256, 1024)
	if err != nil {
		return Config{}, err
	}
	artifactVersionCleanupPasses, err := boundedInt("INGESTION_ARTIFACT_VERSION_CLEANUP_PASSES", 256, 4096)
	if err != nil {
		return Config{}, err
	}
	artifactMaximumShardBytes, err := boundedInt64("INGESTION_ARTIFACT_MAX_SHARD_BYTES", 4<<20, 32<<20)
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
	memoryLimit, err := boundedInt64("INGESTION_MEMORY_LIMIT_BYTES", 2<<30, 64<<30)
	if err != nil {
		return Config{}, err
	}
	parserMemory, err := boundedInt64("INGESTION_PARSER_SANDBOX_MEMORY_BYTES", defaultParserSandboxMemoryBytes, maximumParserSandboxMemoryBytes)
	if err != nil {
		return Config{}, err
	}
	parserRuntimeHeadroomBytes, err := boundedInt64("INGESTION_PARSER_RUNTIME_HEADROOM_BYTES", 256<<20, 4<<30)
	if err != nil {
		return Config{}, err
	}
	if parserMemory < defaultParserSandboxMemoryBytes {
		return Config{}, fmt.Errorf("INGESTION_PARSER_SANDBOX_MEMORY_BYTES must be at least %d", defaultParserSandboxMemoryBytes)
	}
	if int64(workConcurrency)*parserMemory+parserRuntimeHeadroomBytes > memoryLimit {
		return Config{}, fmt.Errorf("INGESTION_WORK_CONCURRENCY exceeds INGESTION_MEMORY_LIMIT_BYTES")
	}
	timeout, err := boundedDuration("INGESTION_PROCESSING_TIMEOUT", time.Minute, 13*time.Minute+30*time.Second, 12*time.Minute+30*time.Second)
	if err != nil {
		return Config{}, err
	}
	persistenceTimeout, err := boundedDuration("INGESTION_PERSISTENCE_TIMEOUT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	artifactAbortTimeout, err := boundedDuration("INGESTION_ARTIFACT_ABORT_TIMEOUT", time.Second, time.Minute, 10*time.Second)
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
	firstRetryDelay, err := boundedDuration("INGESTION_FIRST_RETRY_DELAY", time.Second, 10*time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	secondRetryDelay, err := boundedDuration("INGESTION_SECOND_RETRY_DELAY", time.Second, 10*time.Minute, 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	subsequentRetryDelay, err := boundedDuration("INGESTION_SUBSEQUENT_RETRY_DELAY", time.Second, 10*time.Minute, 2*time.Minute)
	if err != nil {
		return Config{}, err
	}
	outboxInterval, err := boundedDuration("INGESTION_OUTBOX_INTERVAL", 100*time.Millisecond, time.Minute, time.Second)
	if err != nil {
		return Config{}, err
	}
	rabbitDialTimeout, err := boundedDuration("INGESTION_RABBITMQ_DIAL_TIMEOUT", time.Second, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	rabbitHeartbeat, err := boundedDuration("INGESTION_RABBITMQ_HEARTBEAT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	rabbitPublishTimeout, err := boundedDuration("INGESTION_RABBITMQ_PUBLISH_TIMEOUT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	outboxLease, err := boundedDuration("INGESTION_OUTBOX_LEASE", time.Second, 5*time.Minute, 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	retryDispatchDelay, err := boundedDuration("INGESTION_RETRY_DISPATCH_DELAY", time.Second, time.Minute, time.Second)
	if err != nil {
		return Config{}, err
	}
	outboxRetryBaseDelay, err := boundedDuration("INGESTION_OUTBOX_RETRY_BASE_DELAY", time.Second, time.Minute, time.Second)
	if err != nil {
		return Config{}, err
	}
	outboxRetryMaxDelay, err := boundedDuration("INGESTION_OUTBOX_RETRY_MAX_DELAY", time.Second, 10*time.Minute, 5*time.Minute)
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
	workerReadinessProbeTimeout, err := boundedDuration("INGESTION_WORKER_READINESS_PROBE_TIMEOUT", time.Second, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	workerReadinessRefreshInterval, err := boundedDuration("INGESTION_WORKER_READINESS_REFRESH_INTERVAL", 2*time.Second, time.Minute, 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	workerMetricsReadHeaderTimeout, err := boundedDuration("INGESTION_WORKER_METRICS_READ_HEADER_TIMEOUT", 5*time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	workerMetricsShutdownTimeout, err := boundedDuration("INGESTION_WORKER_METRICS_SHUTDOWN_TIMEOUT", 5*time.Second, time.Minute, 10*time.Second)
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
	temporaryDirectory := optional("INGESTION_TEMP_DIR", "/tmp")
	if temporaryDirectory != "/tmp" {
		return Config{}, fmt.Errorf("INGESTION_TEMP_DIR must be /tmp")
	}
	debugDumpPDFTextDirectory := strings.TrimSpace(os.Getenv("INGESTION_DEBUG_DUMP_PDFTEXT_DIR"))
	if debugDumpPDFTextDirectory != "" && (!strings.HasPrefix(debugDumpPDFTextDirectory, "/") || debugDumpPDFTextDirectory == "/" || containsASCIIControl(debugDumpPDFTextDirectory)) {
		return Config{}, fmt.Errorf("INGESTION_DEBUG_DUMP_PDFTEXT_DIR must be an absolute debug directory")
	}
	return Config{
		RuntimeBackend:                 "local",
		DSN:                            dsn,
		RabbitURI:                      rabbitURI,
		MinIOEndpoint:                  endpoint,
		MinIOAccessKey:                 accessKey,
		MinIOSecretKey:                 secretKey,
		SourceBucket:                   sourceBucket,
		ArtifactBucket:                 artifactBucket,
		MinIOCAFile:                    caFile,
		MetricsAddress:                 metrics,
		TokenizerFile:                  tokenizerFile,
		PDFInfoPath:                    optional("INGESTION_PDFINFO_PATH", "/usr/bin/pdfinfo"),
		PDFTextPath:                    optional("INGESTION_PDFTOTEXT_PATH", "/usr/bin/pdftotext"),
		EPUBParserPath:                 optional("INGESTION_EPUB_PARSER_PATH", "/usr/local/bin/epub-parser"),
		TemporaryDirectory:             temporaryDirectory,
		DebugDumpPDFTextDirectory:      debugDumpPDFTextDirectory,
		Queue:                          optional("INGESTION_QUEUE", "ingestion.book-uploaded.v1"),
		ResultExchange:                 optional("INGESTION_RESULT_EXCHANGE", "raglibrarian.ingestion.events.v1"),
		MinIOInsecure:                  insecure,
		WorkConcurrency:                workConcurrency,
		MaximumAttempts:                maximumAttempts,
		MaximumChunks:                  maximumChunks,
		ChunkMaximumTokens:             chunkMaximumTokens,
		ChunkOverlapTokens:             chunkOverlapTokens,
		ChunkTargetPages:               chunkTargetPages,
		ChunkMaximumPages:              chunkMaximumPages,
		MaximumSourceBytes:             maximumSource,
		MaximumExtractedBytes:          maximumExtracted,
		MaximumPageBytes:               maximumPage,
		MaximumManifestBytes:           maximumManifest,
		ArtifactChunksPerShard:         artifactChunksPerShard,
		ArtifactVersionCleanupPasses:   artifactVersionCleanupPasses,
		ArtifactMaximumShardBytes:      artifactMaximumShardBytes,
		MaximumTemporaryBytes:          maximumTemporary,
		MemoryLimitBytes:               memoryLimit,
		ParserSandboxMemoryBytes:       parserMemory,
		ParserRuntimeHeadroomBytes:     parserRuntimeHeadroomBytes,
		MaximumPages:                   maximumPages,
		ProcessingTimeout:              timeout,
		PersistenceTimeout:             persistenceTimeout,
		ArtifactAbortTimeout:           artifactAbortTimeout,
		JobLease:                       lease,
		FirstRetryDelay:                firstRetryDelay,
		SecondRetryDelay:               secondRetryDelay,
		SubsequentRetryDelay:           subsequentRetryDelay,
		OutboxInterval:                 outboxInterval,
		RabbitDialTimeout:              rabbitDialTimeout,
		RabbitHeartbeat:                rabbitHeartbeat,
		RabbitPublishTimeout:           rabbitPublishTimeout,
		OutboxLease:                    outboxLease,
		RetryDispatchDelay:             retryDispatchDelay,
		OutboxRetryBaseDelay:           outboxRetryBaseDelay,
		OutboxRetryMaxDelay:            outboxRetryMaxDelay,
		CleanupInterval:                cleanupInterval,
		OrphanGracePeriod:              orphanGracePeriod,
		WorkerReadinessProbeTimeout:    workerReadinessProbeTimeout,
		WorkerReadinessRefreshInterval: workerReadinessRefreshInterval,
		WorkerMetricsReadHeaderTimeout: workerMetricsReadHeaderTimeout,
		WorkerMetricsShutdownTimeout:   workerMetricsShutdownTimeout,
		RunAs:                          process.Identity{UID: uid, GID: gid},
	}, nil
}

func readSecret(key string, maximum int) (string, error) {
	path, err := required(key)
	if err != nil {
		return "", err
	}
	file, err := process.OpenSecretFile(path, int64(maximum))
	if err != nil {
		return "", fmt.Errorf("%s is invalid", key)
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || len(contents) > maximum || value == "" {
		return "", fmt.Errorf("%s is invalid", key)
	}
	return value, nil
}

func chunkPolicyValues() (int, int, int, int, error) {
	maximumTokens, err := fixedInt("INGESTION_CHUNK_MAX_TOKENS", chunking.DefaultMaximumTokens)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	overlapTokens, err := fixedInt("INGESTION_CHUNK_OVERLAP_TOKENS", chunking.DefaultOverlapTokens)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	targetPages, err := fixedInt("INGESTION_CHUNK_TARGET_PAGES", chunking.DefaultTargetPages)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	maximumPages, err := fixedInt("INGESTION_CHUNK_MAX_PAGES", chunking.DefaultMaximumPages)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return maximumTokens, overlapTokens, targetPages, maximumPages, nil
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

func containsASCIIControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
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

func fixedInt(key string, supported int) (int, error) {
	value, err := strconv.Atoi(optional(key, strconv.Itoa(supported)))
	if err != nil || value != supported {
		return 0, fmt.Errorf("%s must be %d for the supported processing profile", key, supported)
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

func fixedInt64(key string, supported int64) (int64, error) {
	value, err := strconv.ParseInt(optional(key, strconv.FormatInt(supported, 10)), 10, 64)
	if err != nil || value != supported {
		return 0, fmt.Errorf("%s must be %d for the supported processing profile", key, supported)
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

// ValidateServerlessBrokerURI restricts short-lived jobs to private AMQPS.
func ValidateServerlessBrokerURI(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "amqps" || parsed.Host == "" || parsed.User == nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid serverless broker URI")
	}
	host := parsed.Hostname()
	if serverlessBrokerHostAllowed(host,
		optional("INGESTION_SERVERLESS_BROKER_ALLOWED_HOSTS", "localhost,rabbit,rabbitmq"),
		os.Getenv("INGESTION_SERVERLESS_BROKER_ALLOWED_SUFFIXES")) {
		return nil
	}
	if address := net.ParseIP(host); address != nil && (address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast()) {
		return nil
	}
	return fmt.Errorf("serverless broker must be private")
}

func serverlessBrokerHostAllowed(host, allowedHosts, allowedSuffixes string) bool {
	normalizedHost := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, value := range strings.Split(allowedHosts, ",") {
		allowed := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		if allowed != "" && normalizedHost == allowed {
			return true
		}
	}
	for _, value := range strings.Split(allowedSuffixes, ",") {
		suffix := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		if suffix != "" && (normalizedHost == suffix || strings.HasSuffix(normalizedHost, "."+suffix)) {
			return true
		}
	}
	return false
}
