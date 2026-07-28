// Package config loads bounded Answer runtime configuration without exposing secret values.
package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
)

const (
	defaultMaximumQuestionCharacters = 2000
	defaultMaximumFilterTags         = 20
	defaultMaximumTagCharacters      = 64
	defaultMaximumAuthorCharacters   = 256
	defaultMaximumResultLimit        = 20
	defaultMaximumEvidence           = 8
	defaultMaximumContextBytes       = 32 << 10
	defaultMaximumEvidenceBytes      = 8 << 10
	defaultMaximumSegments           = 8
	defaultMaximumAnswerBytes        = 8 << 10
	defaultMaximumSummaryRunes       = 512
	defaultMaximumFailureDetailRunes = 160
	defaultMaximumCitations          = 8
	defaultMaximumOutputTokens       = 768
	defaultProviderConcurrency       = 4
	defaultRequestTimeout            = 5 * time.Minute
	defaultRetrievalTimeout          = 4*time.Minute + 45*time.Second
	defaultProviderTimeout           = 4*time.Minute + 30*time.Second
	DefaultProviderMaxResponseBytes  = 128 << 10
	DefaultProviderMaxCandidateBytes = 32 << 10
)

type Config struct {
	GRPCAddress              string
	MetricsAddress           string
	RetrievalAddress         string
	RetrievalDNSName         string
	Generator                GeneratorConfig
	TLS                      internaltls.Files
	RunAs                    process.Identity
	RequestPolicy            domain.RequestPolicy
	Limits                   application.Limits
	ReadinessProbeTimeout    time.Duration
	ReadinessPollInterval    time.Duration
	ShutdownTimeout          time.Duration
	MetricsMaxHeaderBytes    int
	MetricsReadTimeout       time.Duration
	MetricsReadHeaderTimeout time.Duration
	MetricsWriteTimeout      time.Duration
	MetricsIdleTimeout       time.Duration
}

type GeneratorConfig struct {
	BaseURL           string
	Model             string
	RequestsPerMinute int
	MaxResponseBytes  int
	MaxCandidateBytes int
	HTTPClientTimeout time.Duration
	LogErrorBody      bool
	APIKeyFile        string
	CAFile            string
}

func Load() (Config, error) {
	uid, uidErr := positiveInteger("RUN_AS_UID", 65532, 1, 1<<31-1)
	gid, gidErr := positiveInteger("RUN_AS_GID", 65532, 1, 1<<31-1)
	maximumQuestionCharacters, questionErr := positiveInteger("ANSWER_MAX_QUESTION_CHARACTERS", defaultMaximumQuestionCharacters, 1, 1<<20)
	maximumFilterTags, filterTagsErr := positiveInteger("ANSWER_MAX_FILTER_TAGS", defaultMaximumFilterTags, 1, 256)
	maximumTagCharacters, tagCharactersErr := positiveInteger("ANSWER_MAX_TAG_CHARACTERS", defaultMaximumTagCharacters, 1, 1<<20)
	maximumAuthorCharacters, authorCharactersErr := positiveInteger("ANSWER_MAX_AUTHOR_CHARACTERS", defaultMaximumAuthorCharacters, 1, 1<<20)
	maximumResultLimit, resultLimitErr := positiveInteger("ANSWER_MAX_RESULT_LIMIT", defaultMaximumResultLimit, 1, 256)
	maximumEvidence, evidenceErr := positiveInteger("ANSWER_MAX_EVIDENCE", defaultMaximumEvidence, 1, 64)
	maximumContext, contextErr := positiveInteger("ANSWER_MAX_CONTEXT_BYTES", defaultMaximumContextBytes, 1, 1<<20)
	maximumItem, itemErr := positiveInteger("ANSWER_MAX_EVIDENCE_BYTES", defaultMaximumEvidenceBytes, 1, 1<<20)
	maximumSegments, segmentErr := positiveInteger("ANSWER_MAX_SEGMENTS", defaultMaximumSegments, 1, 64)
	maximumAnswer, answerErr := positiveInteger("ANSWER_MAX_ANSWER_BYTES", defaultMaximumAnswerBytes, 1, 1<<20)
	maximumSummaryRunes, summaryErr := positiveInteger("ANSWER_MAX_SUMMARY_RUNES", defaultMaximumSummaryRunes, 1, 1<<20)
	maximumFailureDetailRunes, failureDetailErr := positiveInteger("ANSWER_MAX_FAILURE_DETAIL_RUNES", defaultMaximumFailureDetailRunes, 1, 1<<20)
	maximumCitations, citationErr := positiveInteger("ANSWER_MAX_CITATIONS_PER_SEGMENT", defaultMaximumCitations, 1, 64)
	maximumTokens, tokenErr := positiveInteger("ANSWER_MAX_OUTPUT_TOKENS", defaultMaximumOutputTokens, 1, 8192)
	concurrency, concurrencyErr := positiveInteger("ANSWER_PROVIDER_CONCURRENCY", defaultProviderConcurrency, 1, 64)
	requestTimeout, requestErr := duration("ANSWER_REQUEST_TIMEOUT", defaultRequestTimeout, 100*time.Millisecond, 5*time.Minute)
	retrievalTimeout, retrievalErr := duration("ANSWER_RETRIEVAL_TIMEOUT", defaultRetrievalTimeout, 100*time.Millisecond, 5*time.Minute)
	providerTimeout, providerErr := duration("ANSWER_PROVIDER_TIMEOUT", defaultProviderTimeout, 100*time.Millisecond, 5*time.Minute)
	providerHTTPTimeout, providerHTTPTimeoutErr := nonNegativeDuration("ANSWER_PROVIDER_HTTP_TIMEOUT", 0, 5*time.Minute)
	readinessProbeTimeout, readinessProbeErr := duration("ANSWER_READINESS_PROBE_TIMEOUT", 2*time.Second, 100*time.Millisecond, time.Minute)
	readinessPollInterval, readinessPollErr := duration("ANSWER_READINESS_POLL_INTERVAL", 2*time.Second, 100*time.Millisecond, time.Minute)
	shutdownTimeout, shutdownErr := duration("ANSWER_SHUTDOWN_TIMEOUT", 3*time.Second, 100*time.Millisecond, time.Minute)
	metricsReadTimeout, metricsReadErr := duration("ANSWER_METRICS_READ_TIMEOUT", 3*time.Second, 100*time.Millisecond, time.Minute)
	metricsReadHeaderTimeout, metricsReadHeaderErr := duration("ANSWER_METRICS_READ_HEADER_TIMEOUT", 2*time.Second, 100*time.Millisecond, time.Minute)
	metricsMaxHeaderBytes, metricsMaxHeaderBytesErr := positiveInteger("ANSWER_METRICS_MAX_HEADER_BYTES", 16<<10, 1, 1<<20)
	metricsWriteTimeout, metricsWriteErr := duration("ANSWER_METRICS_WRITE_TIMEOUT", 5*time.Second, 100*time.Millisecond, time.Minute)
	metricsIdleTimeout, metricsIdleErr := duration("ANSWER_METRICS_IDLE_TIMEOUT", 30*time.Second, time.Second, 5*time.Minute)
	configuration := Config{
		GRPCAddress:      os.Getenv("ANSWER_GRPC_ADDR"),
		MetricsAddress:   os.Getenv("ANSWER_METRICS_ADDR"),
		RetrievalAddress: os.Getenv("ANSWER_RETRIEVAL_GRPC_ADDR"),
		RetrievalDNSName: os.Getenv("ANSWER_RETRIEVAL_TLS_SERVER_NAME"),
		Generator: GeneratorConfig{
			BaseURL:           os.Getenv("ANSWER_LLM_BASE_URL"),
			Model:             os.Getenv("ANSWER_LLM_MODEL"),
			APIKeyFile:        os.Getenv("ANSWER_LLM_API_KEY_FILE"),
			CAFile:            os.Getenv("ANSWER_LLM_CA_FILE"),
			HTTPClientTimeout: providerHTTPTimeout,
		},
		TLS: internaltls.Files{
			CA:          os.Getenv("ANSWER_TLS_CA_FILE"),
			Certificate: os.Getenv("ANSWER_TLS_CERT_FILE"),
			Key:         os.Getenv("ANSWER_TLS_KEY_FILE"),
		},
		RunAs: process.Identity{UID: uid, GID: gid},
		RequestPolicy: domain.RequestPolicy{
			MaximumQuestionCharacters: maximumQuestionCharacters,
			MaximumFilterTags:         maximumFilterTags,
			MaximumTagCharacters:      maximumTagCharacters,
			MaximumAuthorCharacters:   maximumAuthorCharacters,
			MaximumResultLimit:        uint32(maximumResultLimit),
		},
		Limits: application.Limits{MaximumEvidence: maximumEvidence, MaximumContextBytes: maximumContext,
			MaximumEvidenceBytes: maximumItem, MaximumSegments: maximumSegments, MaximumAnswerBytes: maximumAnswer, MaximumSummaryRunes: maximumSummaryRunes,
			MaximumFailureDetailRunes: maximumFailureDetailRunes,
			MaximumCitations:          maximumCitations,
			MaximumOutputTokens:       maximumTokens, ProviderConcurrency: concurrency, RequestTimeout: requestTimeout, RetrievalTimeout: retrievalTimeout, ProviderTimeout: providerTimeout},
		ReadinessProbeTimeout:    readinessProbeTimeout,
		ReadinessPollInterval:    readinessPollInterval,
		ShutdownTimeout:          shutdownTimeout,
		MetricsMaxHeaderBytes:    metricsMaxHeaderBytes,
		MetricsReadTimeout:       metricsReadTimeout,
		MetricsReadHeaderTimeout: metricsReadHeaderTimeout,
		MetricsWriteTimeout:      metricsWriteTimeout,
		MetricsIdleTimeout:       metricsIdleTimeout,
	}
	rpm, rpmErr := providerRequestsPerMinute("ANSWER_PROVIDER_REQUESTS_PER_MINUTE")
	if rpmErr != nil {
		return Config{}, errors.New("invalid answer configuration")
	}
	configuration.Generator.RequestsPerMinute = rpm
	maxResponseBytes, maxResponseBytesErr := positiveInteger("ANSWER_PROVIDER_MAX_RESPONSE_BYTES", DefaultProviderMaxResponseBytes, 1, 1<<20)
	maxCandidateBytes, maxCandidateBytesErr := positiveInteger("ANSWER_PROVIDER_MAX_CANDIDATE_BYTES", DefaultProviderMaxCandidateBytes, 1, 256<<10)
	configuration.Generator.MaxResponseBytes = maxResponseBytes
	configuration.Generator.MaxCandidateBytes = maxCandidateBytes
	logProviderErrorBody, logProviderErrorBodyErr := boolean("ANSWER_PROVIDER_LOG_ERROR_BODY", false)
	if logProviderErrorBodyErr != nil {
		return Config{}, errors.New("invalid answer configuration")
	}
	configuration.Generator.LogErrorBody = logProviderErrorBody
	if configuration.RetrievalDNSName == "" {
		configuration.RetrievalDNSName = "retrieval-service"
	}
	errs := []error{
		uidErr, gidErr, questionErr, filterTagsErr, tagCharactersErr, authorCharactersErr, resultLimitErr,
		evidenceErr, contextErr, itemErr, segmentErr, answerErr, summaryErr, failureDetailErr, citationErr, tokenErr, concurrencyErr,
		maxResponseBytesErr, maxCandidateBytesErr,
		requestErr, retrievalErr, providerErr, readinessProbeErr, readinessPollErr, shutdownErr, metricsReadErr,
		providerHTTPTimeoutErr,
		metricsReadHeaderErr, metricsMaxHeaderBytesErr, metricsWriteErr, metricsIdleErr,
	}
	for _, err := range errs {
		if err != nil {
			return Config{}, errors.New("invalid answer configuration")
		}
	}
	if !validListenAddress(configuration.GRPCAddress) || !validListenAddress(configuration.MetricsAddress) || !validServiceAddress(configuration.RetrievalAddress) ||
		configuration.RetrievalDNSName != "retrieval-service" ||
		!validProviderURL(configuration.Generator.BaseURL) || strings.TrimSpace(configuration.Generator.Model) == "" || len(configuration.Generator.Model) > 256 || strings.ContainsAny(configuration.Generator.Model, "\r\n") ||
		configuration.Generator.APIKeyFile == "" || configuration.TLS.CA == "" || configuration.TLS.Certificate == "" || configuration.TLS.Key == "" ||
		configuration.Generator.MaxCandidateBytes > configuration.Generator.MaxResponseBytes ||
		(configuration.Generator.HTTPClientTimeout > 0 && configuration.Generator.HTTPClientTimeout > configuration.Limits.ProviderTimeout) ||
		configuration.Limits.MaximumEvidenceBytes > configuration.Limits.MaximumContextBytes || configuration.Limits.RetrievalTimeout >= configuration.Limits.RequestTimeout ||
		configuration.Limits.ProviderTimeout >= configuration.Limits.RequestTimeout {
		return Config{}, errors.New("invalid answer configuration")
	}
	return configuration, nil
}

func positiveInteger(key string, fallback, minimum, maximum int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, errors.New("invalid integer")
	}
	return parsed, nil
}

func duration(key string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, errors.New("invalid duration")
	}
	return parsed, nil
}

func nonNegativeDuration(key string, fallback, maximum time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 0 || parsed > maximum {
		return 0, errors.New("invalid duration")
	}
	return parsed, nil
}

func boolean(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, errors.New("invalid boolean")
	}
	return parsed, nil
}

func validListenAddress(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return false
	}
	return host == "" || host == "127.0.0.1" || host == "::1" || net.ParseIP(host) != nil
}

func validServiceAddress(value string) bool {
	host, port, err := net.SplitHostPort(value)
	return err == nil && host != "" && port != "" && !strings.ContainsAny(host, "/?#")
}

func validProviderURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && len(value) <= 2048 && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func providerRequestsPerMinute(key string) (int, error) {
	value := os.Getenv(key)
	if value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return 0, errors.New("invalid integer")
		}
		return parsed, nil
	}
	return 0, nil
}

func optional(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
