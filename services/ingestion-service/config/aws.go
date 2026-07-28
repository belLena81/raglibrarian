package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/belLena81/raglibrarian/pkg/process"
)

func Load() (Config, error) {
	timeout, err := awsLoadTimeout()
	if err != nil {
		return Config{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return LoadContext(ctx)
}

func LoadContext(ctx context.Context) (Config, error) {
	if optional("INGESTION_RUNTIME_BACKEND", "local") == "local" {
		return loadLocal()
	}
	return loadAWS(ctx)
}

func LoadDispatcher() (DispatcherConfig, error) {
	timeout, err := awsLoadTimeout()
	if err != nil {
		return DispatcherConfig{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return LoadDispatcherContext(ctx)
}

func LoadDispatcherContext(ctx context.Context) (DispatcherConfig, error) {
	if optional("INGESTION_RUNTIME_BACKEND", "local") == "local" {
		return loadLocalDispatcher()
	}
	return loadAWSDispatcher(ctx)
}

func LoadCleanup() (CleanupConfig, error) {
	timeout, err := awsLoadTimeout()
	if err != nil {
		return CleanupConfig{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return LoadCleanupContext(ctx)
}

func LoadCleanupContext(ctx context.Context) (CleanupConfig, error) {
	if optional("INGESTION_RUNTIME_BACKEND", "local") == "local" {
		return loadLocalCleanup()
	}
	return loadAWSCleanup(ctx)
}

func loadAWS(ctx context.Context) (Config, error) {
	if os.Getenv("INGESTION_RUNTIME_BACKEND") != "aws" {
		return Config{}, fmt.Errorf("INGESTION_RUNTIME_BACKEND must be local or aws")
	}
	region, err := required("INGESTION_AWS_REGION")
	if err != nil {
		return Config{}, err
	}
	dsn, err := readAWSSecret(ctx, region, "INGESTION_DATABASE_SECRET_ARN", "dsn", 4096)
	if err != nil {
		return Config{}, err
	}
	if err = validateAWSPostgresDSN(dsn); err != nil {
		return Config{}, err
	}
	rabbitURI, err := readAWSSecret(ctx, region, "INGESTION_RABBITMQ_PUBLISHER_SECRET_ARN", "uri", 4096)
	if err != nil {
		return Config{}, err
	}
	if err = validateAWSRabbitURI(rabbitURI); err != nil {
		return Config{}, err
	}
	sourceBucket, err := required("INGESTION_SOURCE_BUCKET")
	if err != nil {
		return Config{}, err
	}
	artifactBucket, err := required("INGESTION_ARTIFACT_BUCKET")
	if err != nil || sourceBucket == artifactBucket {
		return Config{}, fmt.Errorf("source and artifact buckets must be present and differ")
	}
	kmsKey, err := required("INGESTION_KMS_KEY_ARN")
	if err != nil {
		return Config{}, err
	}
	tokenizerFile, err := required("INGESTION_TOKENIZER_FILE")
	if err != nil {
		return Config{}, err
	}
	metrics, err := privateAddress(optional("INGESTION_METRICS_ADDR", "127.0.0.1:9093"))
	if err != nil {
		return Config{}, err
	}
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
	maximumPages, err := boundedInt64("INGESTION_MAX_PAGES", 1000, 1000)
	if err != nil {
		return Config{}, err
	}
	maximumSource, err := fixedInt64("INGESTION_MAX_SOURCE_BYTES", 25<<20)
	if err != nil {
		return Config{}, err
	}
	maximumExtracted, err := boundedInt64("INGESTION_MAX_EXTRACTED_BYTES", 64<<20, 1<<30)
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
	artifactMaximumShardBytes, err := boundedInt64("INGESTION_ARTIFACT_MAX_SHARD_BYTES", 4<<20, 32<<20)
	if err != nil {
		return Config{}, err
	}
	maximumTemporary, err := boundedInt64("INGESTION_MAX_TEMP_BYTES", 2<<30, 10<<30)
	if err != nil || maximumTemporary < maximumSource {
		return Config{}, fmt.Errorf("INGESTION_MAX_TEMP_BYTES is invalid")
	}
	memoryLimit, err := boundedInt64("INGESTION_MEMORY_LIMIT_BYTES", 2<<30, 64<<30)
	if err != nil {
		return Config{}, err
	}
	parserMemory, err := boundedInt64("INGESTION_PARSER_SANDBOX_MEMORY_BYTES", defaultParserSandboxMemoryBytes, maximumParserSandboxMemoryBytes)
	if err != nil {
		return Config{}, err
	}
	if parserMemory < defaultParserSandboxMemoryBytes {
		return Config{}, fmt.Errorf("INGESTION_PARSER_SANDBOX_MEMORY_BYTES must be at least %d", defaultParserSandboxMemoryBytes)
	}
	if int64(workConcurrency)*parserMemory+(256<<20) > memoryLimit {
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
	if err != nil || lease < timeout+30*time.Second {
		return Config{}, fmt.Errorf("INGESTION_JOB_LEASE must exceed processing timeout by at least 30s")
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
	grace, err := boundedDuration("INGESTION_ORPHAN_GRACE_PERIOD", 15*time.Minute, 7*24*time.Hour, time.Hour)
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
	return Config{
		RuntimeBackend:                 "aws",
		DSN:                            dsn,
		RabbitURI:                      rabbitURI,
		SourceBucket:                   sourceBucket,
		ArtifactBucket:                 artifactBucket,
		AWSRegion:                      region,
		KMSKeyARN:                      kmsKey,
		MetricsAddress:                 metrics,
		TokenizerFile:                  tokenizerFile,
		PDFInfoPath:                    optional("INGESTION_PDFINFO_PATH", "/usr/bin/pdfinfo"),
		PDFTextPath:                    optional("INGESTION_PDFTOTEXT_PATH", "/usr/bin/pdftotext"),
		EPUBParserPath:                 optional("INGESTION_EPUB_PARSER_PATH", "/usr/local/bin/epub-parser"),
		TemporaryDirectory:             "/tmp",
		Queue:                          optional("INGESTION_QUEUE", "ingestion.book-uploaded.v1"),
		ResultExchange:                 optional("INGESTION_RESULT_EXCHANGE", "raglibrarian.ingestion.events.v1"),
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
		ArtifactMaximumShardBytes:      artifactMaximumShardBytes,
		MaximumTemporaryBytes:          maximumTemporary,
		MaximumPages:                   uint32(maximumPages), // #nosec G115 -- bounded above.
		MemoryLimitBytes:               memoryLimit,
		ParserSandboxMemoryBytes:       parserMemory,
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
		OrphanGracePeriod:              grace,
		WorkerReadinessProbeTimeout:    workerReadinessProbeTimeout,
		WorkerReadinessRefreshInterval: workerReadinessRefreshInterval,
		WorkerMetricsReadHeaderTimeout: workerMetricsReadHeaderTimeout,
		WorkerMetricsShutdownTimeout:   workerMetricsShutdownTimeout,
		RunAs:                          process.Identity{UID: uid, GID: gid},
	}, nil
}

func loadAWSDispatcher(ctx context.Context) (DispatcherConfig, error) {
	if os.Getenv("INGESTION_RUNTIME_BACKEND") != "aws" {
		return DispatcherConfig{}, fmt.Errorf("INGESTION_RUNTIME_BACKEND must be local or aws")
	}
	region, err := required("INGESTION_AWS_REGION")
	if err != nil {
		return DispatcherConfig{}, err
	}
	dsn, err := readAWSSecret(ctx, region, "INGESTION_DATABASE_SECRET_ARN", "dsn", 4096)
	if err != nil || validateAWSPostgresDSN(dsn) != nil {
		return DispatcherConfig{}, fmt.Errorf("INGESTION_DATABASE_SECRET_ARN is invalid")
	}
	rabbitURI, err := readAWSSecret(ctx, region, "INGESTION_RABBITMQ_PUBLISHER_SECRET_ARN", "uri", 4096)
	if err != nil || validateAWSRabbitURI(rabbitURI) != nil {
		return DispatcherConfig{}, fmt.Errorf("INGESTION_RABBITMQ_PUBLISHER_SECRET_ARN is invalid")
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
	uid, err := boundedInt("RUN_AS_UID", 65532, 1<<30)
	if err != nil {
		return DispatcherConfig{}, err
	}
	gid, err := boundedInt("RUN_AS_GID", 65532, 1<<30)
	if err != nil {
		return DispatcherConfig{}, err
	}
	return DispatcherConfig{
		RuntimeBackend:       "aws",
		DSN:                  dsn,
		RabbitURI:            rabbitURI,
		ResultExchange:       optional("INGESTION_RESULT_EXCHANGE", "raglibrarian.ingestion.events.v1"),
		OutboxInterval:       outboxInterval,
		RabbitDialTimeout:    rabbitDialTimeout,
		RabbitHeartbeat:      rabbitHeartbeat,
		RabbitPublishTimeout: rabbitPublishTimeout,
		OutboxLease:          outboxLease,
		RunAs:                process.Identity{UID: uid, GID: gid},
	}, nil
}

func loadAWSCleanup(ctx context.Context) (CleanupConfig, error) {
	region, err := required("INGESTION_AWS_REGION")
	if err != nil {
		return CleanupConfig{}, err
	}
	dsn, err := readAWSSecret(ctx, region, "INGESTION_DATABASE_SECRET_ARN", "dsn", 4096)
	if err != nil {
		return CleanupConfig{}, err
	}
	if err = validateAWSPostgresDSN(dsn); err != nil {
		return CleanupConfig{}, err
	}
	bucket, err := required("INGESTION_ARTIFACT_BUCKET")
	if err != nil {
		return CleanupConfig{}, err
	}
	kmsKey, err := required("INGESTION_KMS_KEY_ARN")
	if err != nil {
		return CleanupConfig{}, err
	}
	interval, err := boundedDuration("INGESTION_CLEANUP_INTERVAL", time.Minute, 24*time.Hour, 15*time.Minute)
	if err != nil {
		return CleanupConfig{}, err
	}
	grace, err := boundedDuration("INGESTION_ORPHAN_GRACE_PERIOD", 15*time.Minute, 7*24*time.Hour, time.Hour)
	return CleanupConfig{RuntimeBackend: "aws", DSN: dsn, ArtifactBucket: bucket, AWSRegion: region, KMSKeyARN: kmsKey, CleanupInterval: interval, OrphanGracePeriod: grace}, err
}

type secretsAPI interface {
	GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

func readAWSSecret(ctx context.Context, region, arnKey, field string, maximum int) (string, error) {
	arn, err := required(arnKey)
	if err != nil {
		return "", err
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return "", fmt.Errorf("%s is unavailable", arnKey)
	}
	return getSecret(ctx, secretsmanager.NewFromConfig(cfg), arn, arnKey, field, maximum)
}

func getSecret(ctx context.Context, client secretsAPI, arn, label, field string, maximum int) (string, error) {
	timeout, err := awsSecretRequestTimeout()
	if err != nil {
		return "", err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := client.GetSecretValue(requestCtx, &secretsmanager.GetSecretValueInput{SecretId: &arn})
	if err != nil || output.SecretString == nil || len(*output.SecretString) > maximum+128 {
		return "", fmt.Errorf("%s is unavailable", label)
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal([]byte(*output.SecretString), &object); err != nil || len(object) != 1 {
		return "", fmt.Errorf("%s is invalid", label)
	}
	raw, ok := object[field]
	if !ok {
		return "", fmt.Errorf("%s is invalid", label)
	}
	var value string
	if err = json.Unmarshal(raw, &value); err != nil || value == "" || strings.TrimSpace(value) != value || len(value) > maximum {
		return "", fmt.Errorf("%s is invalid", label)
	}
	return value, nil
}

func awsLoadTimeout() (time.Duration, error) {
	return boundedDuration("INGESTION_AWS_LOAD_TIMEOUT", time.Second, time.Minute, 10*time.Second)
}

func awsSecretRequestTimeout() (time.Duration, error) {
	return boundedDuration("INGESTION_AWS_SECRET_REQUEST_TIMEOUT", time.Second, time.Minute, 5*time.Second)
}

func validateAWSPostgresDSN(dsn string) error {
	parsed, err := url.ParseRequestURI(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || parsed.User == nil || parsed.Fragment != "" {
		return errors.New("INGESTION_DATABASE_SECRET_ARN is invalid")
	}
	password, hasPassword := parsed.User.Password()
	sslModes := parsed.Query()["sslmode"]
	if parsed.User.Username() == "" || !hasPassword || password == "" || len(sslModes) != 1 || sslModes[0] != "verify-full" {
		return errors.New("INGESTION_DATABASE_SECRET_ARN is invalid")
	}
	return nil
}

func validateAWSRabbitURI(uri string) error {
	parsed, err := url.ParseRequestURI(uri)
	if err != nil || parsed.Scheme != "amqps" || parsed.Hostname() == "" || parsed.User == nil || parsed.Fragment != "" {
		return errors.New("INGESTION_RABBITMQ_PUBLISHER_SECRET_ARN is invalid")
	}
	password, hasPassword := parsed.User.Password()
	if parsed.User.Username() == "" || !hasPassword || password == "" {
		return errors.New("INGESTION_RABBITMQ_PUBLISHER_SECRET_ARN is invalid")
	}
	return nil
}
