// Package config loads Retrieval runtime configuration without reading secret values.
package config

import (
	"errors"
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

type Config struct {
	GRPCAddress                 string
	MetricsAddress              string
	TEIURL                      string
	TEIRequestsPerSecond        int
	DependencyTimeout           time.Duration
	SearchTimeout               time.Duration
	QdrantURL                   string
	QdrantCollection            string
	QdrantAPIKeyFile            string
	PostgresDSNFile             string
	SummaryLLMBaseURL           string
	SummaryLLMModel             string
	SummaryLLMTimeout           time.Duration
	SummaryLLMMaxOutputTokens   int
	SummaryLLMRequestsPerMinute int
	SummaryLLMAPIKeyFile        string
	SummaryLLMCAFile            string
	TLS                         internaltls.Files
	RunAs                       process.Identity
}

type WorkerConfig struct {
	DSN, ConsumerRabbitURI, PublisherRabbitURI                    string
	MinIOEndpoint, MinIOAccessKey, MinIOSecretKey, ArtifactBucket string
	MinIOInsecure                                                 bool
	TEIURL, QdrantURL, QdrantCollection, QdrantAPIKey             string
	TEIRequestsPerSecond                                          int
	MetricsAddress                                                string
	ServerlessInvocationTimeout                                   time.Duration
	Concurrency                                                   int
	RunAs                                                         process.Identity
}

func Load() (Config, error) {
	grpcAddress := os.Getenv("RETRIEVAL_GRPC_ADDR")
	if grpcAddress == "" {
		grpcAddress = os.Getenv("RETRIEVAL_GRPC_ADDRESS")
	}
	collection := os.Getenv("RETRIEVAL_QDRANT_COLLECTION")
	if collection == "" {
		collection = "evidence_v2"
	}
	uid, uidErr := positiveInteger(os.Getenv("RUN_AS_UID"), 65532)
	gid, gidErr := positiveInteger(os.Getenv("RUN_AS_GID"), 65532)
	configuration := Config{
		GRPCAddress: grpcAddress, MetricsAddress: os.Getenv("RETRIEVAL_METRICS_ADDR"), TEIURL: os.Getenv("RETRIEVAL_TEI_URL"),
		QdrantURL: os.Getenv("RETRIEVAL_QDRANT_URL"), QdrantCollection: collection, QdrantAPIKeyFile: os.Getenv("RETRIEVAL_QDRANT_API_KEY_FILE"),
		PostgresDSNFile: os.Getenv("RETRIEVAL_POSTGRES_DSN_FILE"), SummaryLLMBaseURL: os.Getenv("RETRIEVAL_SUMMARY_LLM_BASE_URL"),
		SummaryLLMModel: os.Getenv("RETRIEVAL_SUMMARY_LLM_MODEL"), SummaryLLMAPIKeyFile: os.Getenv("RETRIEVAL_SUMMARY_LLM_API_KEY_FILE"),
		SummaryLLMCAFile: os.Getenv("RETRIEVAL_SUMMARY_LLM_CA_FILE"),
		TLS:              internaltls.Files{CA: os.Getenv("RETRIEVAL_TLS_CA_FILE"), Certificate: os.Getenv("RETRIEVAL_TLS_CERT_FILE"), Key: os.Getenv("RETRIEVAL_TLS_KEY_FILE")},
		RunAs:            process.Identity{UID: uid, GID: gid},
	}
	searchTimeout, searchTimeoutErr := requiredDuration("RETRIEVAL_SEARCH_TIMEOUT")
	dependencyTimeout, dependencyTimeoutErr := optionalDuration("RETRIEVAL_DEPENDENCY_TIMEOUT", searchTimeout)
	summaryTimeout, summaryTimeoutErr := optionalDuration("RETRIEVAL_SUMMARY_LLM_TIMEOUT", searchTimeout)
	summaryMaxOutputTokens, summaryMaxOutputTokensErr := boundedPositiveInteger("RETRIEVAL_SUMMARY_LLM_MAX_OUTPUT_TOKENS", 64, 256)
	configuration.SearchTimeout = searchTimeout
	configuration.DependencyTimeout = dependencyTimeout
	configuration.SummaryLLMTimeout = summaryTimeout
	configuration.SummaryLLMMaxOutputTokens = summaryMaxOutputTokens
	configuration.SummaryLLMRequestsPerMinute = nonNegativeInteger("RETRIEVAL_SUMMARY_LLM_REQUESTS_PER_MINUTE", 15, 1000)
	configuration.TEIRequestsPerSecond = nonNegativeInteger("RETRIEVAL_TEI_REQUESTS_PER_SECOND", 0, 1000)
	if configuration.GRPCAddress == "" || configuration.QdrantCollection == "" || strings.ContainsAny(configuration.QdrantCollection, "/?#") ||
		configuration.PostgresDSNFile == "" || configuration.QdrantAPIKeyFile == "" || configuration.TLS.CA == "" || configuration.TLS.Certificate == "" || configuration.TLS.Key == "" ||
		!privateServiceURL(configuration.TEIURL) || !privateServiceURL(configuration.QdrantURL) || uidErr != nil || gidErr != nil ||
		searchTimeoutErr != nil || dependencyTimeoutErr != nil || summaryTimeoutErr != nil || summaryMaxOutputTokensErr != nil || configuration.SummaryLLMTimeout > configuration.SearchTimeout ||
		!validSummaryProviderConfiguration(configuration) {
		return Config{}, errors.New("invalid retrieval configuration")
	}
	return configuration, nil
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

func requiredDuration(key string) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, errors.New("invalid duration")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
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
	configuration := WorkerConfig{DSN: dsn, ConsumerRabbitURI: consumerURI, PublisherRabbitURI: publisherURI,
		MinIOEndpoint: os.Getenv("RETRIEVAL_MINIO_ENDPOINT"), MinIOAccessKey: accessKey, MinIOSecretKey: secretKey, ArtifactBucket: os.Getenv("RETRIEVAL_ARTIFACT_BUCKET"), MinIOInsecure: minioInsecure,
		TEIURL: os.Getenv("RETRIEVAL_TEI_URL"), QdrantURL: os.Getenv("RETRIEVAL_QDRANT_URL"), QdrantCollection: "evidence_v2", QdrantAPIKey: qdrantAPIKey,
		TEIRequestsPerSecond: nonNegativeInteger("RETRIEVAL_TEI_REQUESTS_PER_SECOND", 0, 1000),
		MetricsAddress:       optional("RETRIEVAL_WORKER_METRICS_ADDR", os.Getenv("RETRIEVAL_METRICS_ADDR")), ServerlessInvocationTimeout: serverlessInvocationTimeout, Concurrency: concurrency, RunAs: process.Identity{UID: uid, GID: gid}}
	if uidErr != nil || gidErr != nil || concurrencyErr != nil || concurrency > 16 || insecureErr != nil || timeoutErr != nil || configuration.MinIOEndpoint == "" ||
		configuration.ArtifactBucket == "" || configuration.MetricsAddress == "" || !privateServiceURL(configuration.TEIURL) || !privateServiceURL(configuration.QdrantURL) {
		return WorkerConfig{}, errors.New("invalid retrieval worker configuration")
	}
	return configuration, nil
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

func nonNegativeInteger(key string, fallback, maximum int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 || parsed > maximum {
		return fallback
	}
	return parsed
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
