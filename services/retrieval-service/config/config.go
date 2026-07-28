// Package config loads Retrieval runtime configuration without reading secret values.
package config

import (
	"errors"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
)

const (
	defaultMinimumSearchScore      = 0.6
	minimumSearchScoreKey          = "RETRIEVAL_MINIMUM_SEARCH_SCORE"
	DefaultQdrantCollection        = "evidence_v2"
	defaultSummaryLLMMaxCalls      = 100
	defaultSummaryLLMOutputMode    = "json_or_plain"
	summaryLLMOutputModeStrictJSON = "strict_json"
	defaultCleanupJobTimeout       = 90 * time.Second
	defaultCleanupJobBatchSize     = 64
)

type Config struct {
	GRPCAddress                 string
	MetricsAddress              string
	FinalizationLease           time.Duration
	ReadinessProbeTimeout       time.Duration
	ReadinessReadHeaderTimeout  time.Duration
	ReadinessIdleTimeout        time.Duration
	ReadinessShutdownTimeout    time.Duration
	TEIURL                      string
	TEIRequestsPerSecond        int
	DependencyTimeout           time.Duration
	SearchTimeout               time.Duration
	MinimumSearchScore          float64
	QdrantURL                   string
	QdrantCollection            string
	QdrantAPIKeyFile            string
	PostgresDSNFile             string
	SummaryLLMBaseURL           string
	SummaryLLMModel             string
	SummaryLLMTimeout           time.Duration
	SummaryLLMMaxOutputTokens   int
	SummaryLLMMaxCalls          int
	SummaryLLMRequestsPerMinute int
	SummaryLLMOutputMode        string
	SummaryLLMAPIKeyFile        string
	SummaryLLMCAFile            string
	TEILogRawResponse           bool
	TEILogRawResponseMaxBytes   int
	TLS                         internaltls.Files
	RunAs                       process.Identity
}

type WorkerConfig struct {
	DSN, ConsumerRabbitURI, PublisherRabbitURI                    string
	MinIOEndpoint, MinIOAccessKey, MinIOSecretKey, ArtifactBucket string
	MinIOInsecure                                                 bool
	TEIURL, QdrantURL, QdrantCollection, QdrantAPIKey             string
	TEIRequestsPerSecond                                          int
	TEILogRawResponse                                             bool
	TEILogRawResponseMaxBytes                                     int
	MinimumSearchScore                                            float64
	MetricsAddress                                                string
	DBPingTimeout                                                 time.Duration
	DependencyTimeout                                             time.Duration
	CollectionEnsureTimeout                                       time.Duration
	ReadinessInitialDelay                                         time.Duration
	ReadinessMaxDelay                                             time.Duration
	ReadinessMaxAttempts                                          int
	ReadinessProbeTimeout                                         time.Duration
	ReconnectInitialBackoff                                       time.Duration
	ReconnectMaxBackoff                                           time.Duration
	DispatchInterval                                              time.Duration
	FinalizationLease                                             time.Duration
	CleanupInterval                                               time.Duration
	CleanupTimeout                                                time.Duration
	CleanupBatchSize                                              int
	StaleBatchAge                                                 time.Duration
	FailureRecordTimeout                                          time.Duration
	PublishTimeout                                                time.Duration
	RabbitDialTimeout                                             time.Duration
	RabbitHeartbeat                                               time.Duration
	ReadinessReadHeaderTimeout                                    time.Duration
	ReadinessIdleTimeout                                          time.Duration
	ReadinessShutdownTimeout                                      time.Duration
	ServerlessInvocationTimeout                                   time.Duration
	Concurrency                                                   int
	RunAs                                                         process.Identity
}

type LambdaRuntimePolicy struct {
	DependencyTimeout       time.Duration
	CollectionEnsureTimeout time.Duration
	FinalizationLease       time.Duration
	StaleBatchAge           time.Duration
	CleanupBatchSize        int
	FailureRecordTimeout    time.Duration
	RabbitDialTimeout       time.Duration
	RabbitHeartbeat         time.Duration
	EndpointResolveTimeout  time.Duration
}

type CleanupJobPolicy struct {
	DependencyTimeout time.Duration
	BatchSize         int
}

func Load() (Config, error) {
	grpcAddress := os.Getenv("RETRIEVAL_GRPC_ADDR")
	if grpcAddress == "" {
		grpcAddress = os.Getenv("RETRIEVAL_GRPC_ADDRESS")
	}
	collection := os.Getenv("RETRIEVAL_QDRANT_COLLECTION")
	if collection == "" {
		collection = DefaultQdrantCollection
	}
	uid, uidErr := positiveInteger(os.Getenv("RUN_AS_UID"), 65532)
	gid, gidErr := positiveInteger(os.Getenv("RUN_AS_GID"), 65532)
	finalizationLease, finalizationLeaseErr := optionalDuration("RETRIEVAL_FINALIZATION_LEASE", 15*time.Minute)
	configuration := Config{
		GRPCAddress: grpcAddress, MetricsAddress: os.Getenv("RETRIEVAL_METRICS_ADDR"), FinalizationLease: finalizationLease,
		TEIURL:    os.Getenv("RETRIEVAL_TEI_URL"),
		QdrantURL: os.Getenv("RETRIEVAL_QDRANT_URL"), QdrantCollection: collection, QdrantAPIKeyFile: os.Getenv("RETRIEVAL_QDRANT_API_KEY_FILE"),
		PostgresDSNFile: os.Getenv("RETRIEVAL_POSTGRES_DSN_FILE"), SummaryLLMBaseURL: os.Getenv("RETRIEVAL_SUMMARY_LLM_BASE_URL"),
		SummaryLLMModel: os.Getenv("RETRIEVAL_SUMMARY_LLM_MODEL"), SummaryLLMAPIKeyFile: os.Getenv("RETRIEVAL_SUMMARY_LLM_API_KEY_FILE"),
		SummaryLLMCAFile: os.Getenv("RETRIEVAL_SUMMARY_LLM_CA_FILE"), SummaryLLMOutputMode: strings.ToLower(strings.TrimSpace(os.Getenv("RETRIEVAL_SUMMARY_LLM_OUTPUT_MODE"))),
		TLS:   internaltls.Files{CA: os.Getenv("RETRIEVAL_TLS_CA_FILE"), Certificate: os.Getenv("RETRIEVAL_TLS_CERT_FILE"), Key: os.Getenv("RETRIEVAL_TLS_KEY_FILE")},
		RunAs: process.Identity{UID: uid, GID: gid},
	}
	searchTimeout, searchTimeoutErr := optionalDuration("RETRIEVAL_SEARCH_TIMEOUT", 2*time.Minute)
	dependencyTimeout, dependencyTimeoutErr := optionalDuration("RETRIEVAL_DEPENDENCY_TIMEOUT", searchTimeout)
	summaryTimeoutDefault := searchTimeout / 2
	if summaryTimeoutDefault <= 0 {
		summaryTimeoutDefault = time.Nanosecond
	}
	summaryTimeout, summaryTimeoutErr := optionalDuration("RETRIEVAL_SUMMARY_LLM_TIMEOUT", summaryTimeoutDefault)
	summaryMaxOutputTokens, summaryMaxOutputTokensErr := boundedPositiveInteger("RETRIEVAL_SUMMARY_LLM_MAX_OUTPUT_TOKENS", 64, 256)
	summaryMaxCalls, summaryMaxCallsErr := nonNegativeInteger("RETRIEVAL_SUMMARY_LLM_MAX_CALLS", defaultSummaryLLMMaxCalls, 1000)
	minimumSearchScore, minimumSearchScoreErr := LoadMinimumSearchScore()
	summaryLLMRequestsPerMinute, summaryLLMRequestsPerMinuteErr := nonNegativeInteger("RETRIEVAL_SUMMARY_LLM_REQUESTS_PER_MINUTE", 15, 1000)
	summaryLLMOutputMode, summaryLLMOutputModeErr := summaryOutputMode(configuration.SummaryLLMOutputMode)
	teiRequestsPerSecond, teiRequestsPerSecondErr := nonNegativeInteger("RETRIEVAL_TEI_REQUESTS_PER_SECOND", 0, 1000)
	teiLogRawResponse, teiLogRawResponseErr := optionalBool("RETRIEVAL_TEI_LOG_RAW_RESPONSE", false)
	teiLogRawResponseMaxBytes, teiLogRawResponseMaxBytesErr := nonNegativeInteger("RETRIEVAL_TEI_LOG_RAW_RESPONSE_MAX_BYTES", 4096, 64<<10)
	readinessProbeTimeout, readinessProbeTimeoutErr := optionalDuration("RETRIEVAL_READY_PROBE_TIMEOUT", 2*time.Second)
	readinessReadHeaderTimeout, readinessReadHeaderTimeoutErr := optionalDuration("RETRIEVAL_READY_READ_HEADER_TIMEOUT", 2*time.Second)
	readinessIdleTimeout, readinessIdleTimeoutErr := optionalDuration("RETRIEVAL_READY_IDLE_TIMEOUT", 30*time.Second)
	readinessShutdownTimeout, readinessShutdownTimeoutErr := optionalDuration("RETRIEVAL_READY_SHUTDOWN_TIMEOUT", 3*time.Second)
	configuration.SearchTimeout = searchTimeout
	configuration.DependencyTimeout = dependencyTimeout
	configuration.SummaryLLMTimeout = summaryTimeout
	configuration.SummaryLLMMaxOutputTokens = summaryMaxOutputTokens
	configuration.SummaryLLMMaxCalls = summaryMaxCalls
	configuration.MinimumSearchScore = minimumSearchScore
	configuration.SummaryLLMRequestsPerMinute = summaryLLMRequestsPerMinute
	configuration.SummaryLLMOutputMode = summaryLLMOutputMode
	configuration.TEIRequestsPerSecond = teiRequestsPerSecond
	configuration.TEILogRawResponse = teiLogRawResponse
	configuration.TEILogRawResponseMaxBytes = teiLogRawResponseMaxBytes
	configuration.ReadinessProbeTimeout = readinessProbeTimeout
	configuration.ReadinessReadHeaderTimeout = readinessReadHeaderTimeout
	configuration.ReadinessIdleTimeout = readinessIdleTimeout
	configuration.ReadinessShutdownTimeout = readinessShutdownTimeout
	if configuration.GRPCAddress == "" || configuration.QdrantCollection == "" || strings.ContainsAny(configuration.QdrantCollection, "/?#") ||
		configuration.PostgresDSNFile == "" || configuration.QdrantAPIKeyFile == "" || configuration.TLS.CA == "" || configuration.TLS.Certificate == "" || configuration.TLS.Key == "" ||
		!privateServiceURL(configuration.TEIURL) || !privateServiceURL(configuration.QdrantURL) || uidErr != nil || gidErr != nil || finalizationLeaseErr != nil ||
		searchTimeoutErr != nil || dependencyTimeoutErr != nil || summaryTimeoutErr != nil || summaryMaxOutputTokensErr != nil || summaryMaxCallsErr != nil || minimumSearchScoreErr != nil || summaryLLMRequestsPerMinuteErr != nil || summaryLLMOutputModeErr != nil || teiRequestsPerSecondErr != nil || teiLogRawResponseErr != nil || teiLogRawResponseMaxBytesErr != nil ||
		readinessProbeTimeoutErr != nil || readinessReadHeaderTimeoutErr != nil || readinessIdleTimeoutErr != nil || readinessShutdownTimeoutErr != nil ||
		configuration.SummaryLLMTimeout >= configuration.SearchTimeout ||
		!validSummaryProviderConfiguration(configuration) {
		return Config{}, errors.New("invalid retrieval configuration")
	}
	return configuration, nil
}

func summaryOutputMode(value string) (string, error) {
	if value == "" {
		return defaultSummaryLLMOutputMode, nil
	}
	if value != defaultSummaryLLMOutputMode && value != summaryLLMOutputModeStrictJSON {
		return "", errors.New("invalid summary provider output mode")
	}
	return value, nil
}

func validSummaryProviderConfiguration(configuration Config) bool {
	if configuration.SummaryLLMBaseURL == "" {
		return true
	}
	return validProviderURL(configuration.SummaryLLMBaseURL) && strings.TrimSpace(configuration.SummaryLLMModel) != "" && len(configuration.SummaryLLMModel) <= 256 &&
		!strings.ContainsAny(configuration.SummaryLLMModel, "\r\n") && configuration.SummaryLLMAPIKeyFile != ""
}

func positiveInteger(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, errors.New("invalid process identity")
	}
	return parsed, nil
}

func boundedDuration(value string, minimum, maximum, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, errors.New("invalid duration")
	}
	return parsed, nil
}

func optionalDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, errors.New("invalid duration")
	}
	return parsed, nil
}

func LoadMinimumSearchScore() (float64, error) {
	return boundedFloat(minimumSearchScoreKey, defaultMinimumSearchScore, 0, 1)
}

func boundedFloat(key string, fallback, minimum, maximum float64) (float64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed <= minimum || parsed > maximum {
		return 0, errors.New("invalid float")
	}
	return parsed, nil
}

func boundedPositiveInteger(key string, fallback, maximum int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > maximum {
		return 0, errors.New("invalid integer")
	}
	return parsed, nil
}

func LoadWorker() (WorkerConfig, error) {
	indexProfile := os.Getenv("RETRIEVAL_INDEX_PROFILE")
	if os.Getenv("RETRIEVAL_PROCESSING_MODE") != "worker" ||
		indexProfile != "m8-bge-v1" {
		return WorkerConfig{}, errors.New("invalid retrieval processing mode")
	}
	dsn, err := readSecretFile("RETRIEVAL_POSTGRES_DSN_FILE", 4096)
	if err != nil {
		return WorkerConfig{}, err
	}
	consumerURI, err := readSecretFile("RETRIEVAL_RABBITMQ_CONSUMER_URI_FILE", 4096)
	if err != nil {
		return WorkerConfig{}, err
	}
	publisherURI, err := readSecretFile("RETRIEVAL_RABBITMQ_PUBLISHER_URI_FILE", 4096)
	if err != nil {
		return WorkerConfig{}, err
	}
	accessKey, err := readSecretFile("RETRIEVAL_MINIO_ACCESS_KEY_FILE", 1024)
	if err != nil {
		return WorkerConfig{}, err
	}
	secretKey, err := readSecretFile("RETRIEVAL_MINIO_SECRET_KEY_FILE", 1024)
	if err != nil {
		return WorkerConfig{}, err
	}
	qdrantAPIKey, err := readSecretFile("RETRIEVAL_QDRANT_API_KEY_FILE", 1024)
	if err != nil {
		return WorkerConfig{}, err
	}
	uid, uidErr := positiveInteger(os.Getenv("RUN_AS_UID"), 65532)
	gid, gidErr := positiveInteger(os.Getenv("RUN_AS_GID"), 65532)
	concurrency, concurrencyErr := positiveInteger(os.Getenv("RETRIEVAL_WORK_CONCURRENCY"), 1)
	minioInsecure, insecureErr := strconv.ParseBool(os.Getenv("RETRIEVAL_MINIO_INSECURE"))
	serverlessInvocationTimeout, timeoutErr := boundedDuration(os.Getenv("RETRIEVAL_SERVERLESS_INVOCATION_TIMEOUT"), 10*time.Second, 13*time.Minute, 3*time.Minute)
	dbPingTimeout, dbPingTimeoutErr := optionalDuration("RETRIEVAL_WORKER_DB_PING_TIMEOUT", 5*time.Second)
	dependencyTimeout, dependencyTimeoutErr := optionalDuration("RETRIEVAL_WORKER_DEPENDENCY_TIMEOUT", 90*time.Second)
	collectionEnsureTimeout, collectionEnsureTimeoutErr := optionalDuration("RETRIEVAL_WORKER_COLLECTION_TIMEOUT", 10*time.Second)
	finalizationLease, finalizationLeaseErr := optionalDuration("RETRIEVAL_FINALIZATION_LEASE", 15*time.Minute)
	readinessInitialDelay, readinessInitialDelayErr := optionalDuration("RETRIEVAL_WORKER_READINESS_INITIAL_DELAY", time.Second)
	readinessMaxDelay, readinessMaxDelayErr := optionalDuration("RETRIEVAL_WORKER_READINESS_MAX_DELAY", 10*time.Second)
	readinessMaxAttempts, readinessMaxAttemptsErr := positiveInteger(os.Getenv("RETRIEVAL_WORKER_READINESS_MAX_ATTEMPTS"), 90)
	readinessProbeTimeout, readinessProbeTimeoutErr := optionalDuration("RETRIEVAL_WORKER_READINESS_PROBE_TIMEOUT", 2*time.Second)
	reconnectInitialBackoff, reconnectInitialBackoffErr := optionalDuration("RETRIEVAL_WORKER_RECONNECT_INITIAL_BACKOFF", time.Second)
	reconnectMaxBackoff, reconnectMaxBackoffErr := optionalDuration("RETRIEVAL_WORKER_RECONNECT_MAX_BACKOFF", 30*time.Second)
	dispatchInterval, dispatchIntervalErr := optionalDuration("RETRIEVAL_WORKER_DISPATCH_INTERVAL", 500*time.Millisecond)
	cleanupInterval, cleanupIntervalErr := optionalDuration("RETRIEVAL_WORKER_CLEANUP_INTERVAL", 15*time.Minute)
	cleanupTimeout, cleanupTimeoutErr := optionalDuration("RETRIEVAL_WORKER_CLEANUP_TIMEOUT", 30*time.Second)
	cleanupBatchSize, cleanupBatchSizeErr := boundedPositiveInteger("RETRIEVAL_WORKER_CLEANUP_BATCH_SIZE", defaultCleanupJobBatchSize, 1024)
	staleBatchAge, staleBatchAgeErr := optionalDuration("RETRIEVAL_WORKER_STALE_BATCH_AGE", 15*time.Minute)
	failureRecordTimeout, failureRecordTimeoutErr := optionalDuration("RETRIEVAL_WORKER_FAILURE_RECORD_TIMEOUT", 10*time.Second)
	publishTimeout, publishTimeoutErr := optionalDuration("RETRIEVAL_WORKER_PUBLISH_TIMEOUT", 10*time.Second)
	rabbitDialTimeout, rabbitDialTimeoutErr := optionalDuration("RETRIEVAL_WORKER_RABBITMQ_DIAL_TIMEOUT", 5*time.Second)
	rabbitHeartbeat, rabbitHeartbeatErr := optionalDuration("RETRIEVAL_WORKER_RABBITMQ_HEARTBEAT", 10*time.Second)
	readinessReadHeaderTimeout, readinessReadHeaderTimeoutErr := optionalDuration("RETRIEVAL_WORKER_READY_READ_HEADER_TIMEOUT", 2*time.Second)
	readinessIdleTimeout, readinessIdleTimeoutErr := optionalDuration("RETRIEVAL_WORKER_READY_IDLE_TIMEOUT", 30*time.Second)
	readinessShutdownTimeout, readinessShutdownTimeoutErr := optionalDuration("RETRIEVAL_WORKER_READY_SHUTDOWN_TIMEOUT", 3*time.Second)
	teiRequestsPerSecond, teiRequestsPerSecondErr := nonNegativeInteger("RETRIEVAL_TEI_REQUESTS_PER_SECOND", 0, 1000)
	teiLogRawResponse, teiLogRawResponseErr := optionalBool("RETRIEVAL_TEI_LOG_RAW_RESPONSE", false)
	teiLogRawResponseMaxBytes, teiLogRawResponseMaxBytesErr := nonNegativeInteger("RETRIEVAL_TEI_LOG_RAW_RESPONSE_MAX_BYTES", 4096, 64<<10)
	minimumSearchScore, minimumSearchScoreErr := LoadMinimumSearchScore()
	configuration := WorkerConfig{DSN: dsn, ConsumerRabbitURI: consumerURI, PublisherRabbitURI: publisherURI,
		MinIOEndpoint: os.Getenv("RETRIEVAL_MINIO_ENDPOINT"), MinIOAccessKey: accessKey, MinIOSecretKey: secretKey, ArtifactBucket: os.Getenv("RETRIEVAL_ARTIFACT_BUCKET"), MinIOInsecure: minioInsecure,
		TEIURL: os.Getenv("RETRIEVAL_TEI_URL"), QdrantURL: os.Getenv("RETRIEVAL_QDRANT_URL"), QdrantCollection: optional("RETRIEVAL_QDRANT_COLLECTION", DefaultQdrantCollection), QdrantAPIKey: qdrantAPIKey,
		TEIRequestsPerSecond: teiRequestsPerSecond, TEILogRawResponse: teiLogRawResponse, TEILogRawResponseMaxBytes: teiLogRawResponseMaxBytes,
		MinimumSearchScore: minimumSearchScore, DBPingTimeout: dbPingTimeout, DependencyTimeout: dependencyTimeout, CollectionEnsureTimeout: collectionEnsureTimeout, FinalizationLease: finalizationLease,
		ReadinessInitialDelay: readinessInitialDelay, ReadinessMaxDelay: readinessMaxDelay, ReadinessMaxAttempts: readinessMaxAttempts, ReadinessProbeTimeout: readinessProbeTimeout,
		ReconnectInitialBackoff: reconnectInitialBackoff, ReconnectMaxBackoff: reconnectMaxBackoff, DispatchInterval: dispatchInterval, CleanupInterval: cleanupInterval,
		CleanupTimeout: cleanupTimeout, CleanupBatchSize: cleanupBatchSize, StaleBatchAge: staleBatchAge, FailureRecordTimeout: failureRecordTimeout, PublishTimeout: publishTimeout,
		RabbitDialTimeout: rabbitDialTimeout, RabbitHeartbeat: rabbitHeartbeat,
		ReadinessReadHeaderTimeout: readinessReadHeaderTimeout, ReadinessIdleTimeout: readinessIdleTimeout, ReadinessShutdownTimeout: readinessShutdownTimeout,
		MetricsAddress: optional("RETRIEVAL_WORKER_METRICS_ADDR", os.Getenv("RETRIEVAL_METRICS_ADDR")), ServerlessInvocationTimeout: serverlessInvocationTimeout, Concurrency: concurrency, RunAs: process.Identity{UID: uid, GID: gid}}
	configuration.TEIRequestsPerSecond = teiRequestsPerSecond
	if uidErr != nil || gidErr != nil || concurrencyErr != nil || concurrency > 16 || insecureErr != nil || timeoutErr != nil ||
		dbPingTimeoutErr != nil || dependencyTimeoutErr != nil || collectionEnsureTimeoutErr != nil || finalizationLeaseErr != nil || readinessInitialDelayErr != nil || readinessMaxDelayErr != nil ||
		readinessMaxAttemptsErr != nil || readinessProbeTimeoutErr != nil || reconnectInitialBackoffErr != nil || reconnectMaxBackoffErr != nil ||
		dispatchIntervalErr != nil || cleanupIntervalErr != nil || cleanupTimeoutErr != nil || cleanupBatchSizeErr != nil || staleBatchAgeErr != nil || failureRecordTimeoutErr != nil || publishTimeoutErr != nil ||
		rabbitDialTimeoutErr != nil || rabbitHeartbeatErr != nil ||
		readinessReadHeaderTimeoutErr != nil || readinessIdleTimeoutErr != nil || readinessShutdownTimeoutErr != nil ||
		teiRequestsPerSecondErr != nil || teiLogRawResponseErr != nil || teiLogRawResponseMaxBytesErr != nil || minimumSearchScoreErr != nil ||
		configuration.ReadinessInitialDelay > configuration.ReadinessMaxDelay || configuration.ReconnectInitialBackoff > configuration.ReconnectMaxBackoff ||
		configuration.MinIOEndpoint == "" ||
		configuration.ArtifactBucket == "" || configuration.MetricsAddress == "" || !privateServiceURL(configuration.TEIURL) || !privateServiceURL(configuration.QdrantURL) {
		return WorkerConfig{}, errors.New("invalid retrieval worker configuration")
	}
	return configuration, nil
}

func LoadLambdaRuntimePolicy() (LambdaRuntimePolicy, error) {
	dependencyTimeout, dependencyTimeoutErr := optionalDuration("RETRIEVAL_LAMBDA_DEPENDENCY_TIMEOUT", 90*time.Second)
	collectionEnsureTimeout, collectionEnsureTimeoutErr := optionalDuration("RETRIEVAL_LAMBDA_COLLECTION_TIMEOUT", 10*time.Second)
	finalizationLease, finalizationLeaseErr := optionalDuration("RETRIEVAL_FINALIZATION_LEASE", 15*time.Minute)
	staleBatchAge, staleBatchAgeErr := optionalDuration("RETRIEVAL_LAMBDA_STALE_BATCH_AGE", 15*time.Minute)
	cleanupBatchSize, cleanupBatchSizeErr := boundedPositiveInteger("RETRIEVAL_LAMBDA_CLEANUP_BATCH_SIZE", defaultCleanupJobBatchSize, 1024)
	failureRecordTimeout, failureRecordTimeoutErr := optionalDuration("RETRIEVAL_LAMBDA_FAILURE_RECORD_TIMEOUT", 10*time.Second)
	rabbitDialTimeout, rabbitDialTimeoutErr := optionalDuration("RETRIEVAL_LAMBDA_RABBITMQ_DIAL_TIMEOUT", 5*time.Second)
	rabbitHeartbeat, rabbitHeartbeatErr := optionalDuration("RETRIEVAL_LAMBDA_RABBITMQ_HEARTBEAT", 10*time.Second)
	endpointResolveTimeout, endpointResolveTimeoutErr := optionalDuration("RETRIEVAL_LAMBDA_ENDPOINT_RESOLVE_TIMEOUT", 3*time.Second)
	if dependencyTimeoutErr != nil || collectionEnsureTimeoutErr != nil || finalizationLeaseErr != nil || staleBatchAgeErr != nil || cleanupBatchSizeErr != nil || failureRecordTimeoutErr != nil ||
		rabbitDialTimeoutErr != nil || rabbitHeartbeatErr != nil || endpointResolveTimeoutErr != nil {
		return LambdaRuntimePolicy{}, errors.New("invalid retrieval lambda runtime policy")
	}
	return LambdaRuntimePolicy{
		DependencyTimeout:       dependencyTimeout,
		CollectionEnsureTimeout: collectionEnsureTimeout,
		FinalizationLease:       finalizationLease,
		StaleBatchAge:           staleBatchAge,
		CleanupBatchSize:        cleanupBatchSize,
		FailureRecordTimeout:    failureRecordTimeout,
		RabbitDialTimeout:       rabbitDialTimeout,
		RabbitHeartbeat:         rabbitHeartbeat,
		EndpointResolveTimeout:  endpointResolveTimeout,
	}, nil
}

func LoadCleanupJobPolicy() (CleanupJobPolicy, error) {
	dependencyTimeout, dependencyTimeoutErr := optionalDuration("RETRIEVAL_CLEANUP_JOB_DEPENDENCY_TIMEOUT", defaultCleanupJobTimeout)
	batchSize, batchSizeErr := boundedPositiveInteger("RETRIEVAL_CLEANUP_JOB_BATCH_SIZE", defaultCleanupJobBatchSize, 1024)
	if dependencyTimeoutErr != nil || batchSizeErr != nil {
		return CleanupJobPolicy{}, errors.New("invalid retrieval cleanup job policy")
	}
	return CleanupJobPolicy{
		DependencyTimeout: dependencyTimeout,
		BatchSize:         batchSize,
	}, nil
}

func readSecretFile(key string, maximum int64) (string, error) {
	path := os.Getenv(key)
	if path == "" {
		return "", errors.New("missing secret file")
	}
	file, err := process.OpenSecretFile(path, maximum)
	if err != nil {
		return "", errors.New("invalid secret file")
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid secret file")
	}
	return value, nil
}

func privateServiceURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host := parsed.Hostname()
	address := net.ParseIP(host)
	if host == "localhost" || (address != nil && (address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast())) {
		return true
	}
	return !strings.Contains(host, ".")
}

func validProviderURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && len(value) <= 2048 && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func nonNegativeInteger(key string, fallback, maximum int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 || parsed > maximum {
		return 0, errors.New("invalid integer")
	}
	return parsed, nil
}

func optionalBool(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, errors.New("invalid boolean")
	}
	return parsed, nil
}

// ValidateServerlessBrokerURI restricts short-lived jobs to private AMQPS.
func ValidateServerlessBrokerURI(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "amqps" || parsed.Host == "" || parsed.User == nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid serverless broker URI")
	}
	host := parsed.Hostname()
	if serverlessBrokerHostAllowed(host,
		optional("RETRIEVAL_SERVERLESS_BROKER_ALLOWED_HOSTS", "localhost,rabbit,rabbitmq"),
		os.Getenv("RETRIEVAL_SERVERLESS_BROKER_ALLOWED_SUFFIXES")) {
		return nil
	}
	if address := net.ParseIP(host); address != nil && (address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast()) {
		return nil
	}
	return errors.New("serverless broker must be private")
}

func optional(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
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
