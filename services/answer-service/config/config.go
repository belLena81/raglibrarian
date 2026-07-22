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
)

type Config struct {
	GRPCAddress      string
	MetricsAddress   string
	RetrievalAddress string
	RetrievalDNSName string
	LLMBaseURL       string
	LLMModel         string
	LLMAPIKeyFile    string
	LLMCAFile        string
	TLS              internaltls.Files
	RunAs            process.Identity
	Limits           application.Limits
}

func Load() (Config, error) {
	defaults := application.DefaultLimits()
	uid, uidErr := positiveInteger("RUN_AS_UID", 65532, 1, 1<<31-1)
	gid, gidErr := positiveInteger("RUN_AS_GID", 65532, 1, 1<<31-1)
	maximumEvidence, evidenceErr := positiveInteger("ANSWER_MAX_EVIDENCE", defaults.MaximumEvidence, 1, 64)
	maximumContext, contextErr := positiveInteger("ANSWER_MAX_CONTEXT_BYTES", defaults.MaximumContextBytes, 1, 1<<20)
	maximumItem, itemErr := positiveInteger("ANSWER_MAX_EVIDENCE_BYTES", defaults.MaximumEvidenceBytes, 1, 1<<20)
	maximumSegments, segmentErr := positiveInteger("ANSWER_MAX_SEGMENTS", defaults.MaximumSegments, 1, 64)
	maximumAnswer, answerErr := positiveInteger("ANSWER_MAX_ANSWER_BYTES", defaults.MaximumAnswerBytes, 1, 1<<20)
	maximumCitations, citationErr := positiveInteger("ANSWER_MAX_CITATIONS_PER_SEGMENT", defaults.MaximumCitations, 1, 64)
	maximumTokens, tokenErr := positiveInteger("ANSWER_MAX_OUTPUT_TOKENS", defaults.MaximumOutputTokens, 1, 8192)
	concurrency, concurrencyErr := positiveInteger("ANSWER_PROVIDER_CONCURRENCY", defaults.ProviderConcurrency, 1, 64)
	requestTimeout, requestErr := duration("ANSWER_REQUEST_TIMEOUT", defaults.RequestTimeout, 100*time.Millisecond, 30*time.Second)
	retrievalTimeout, retrievalErr := duration("ANSWER_RETRIEVAL_TIMEOUT", defaults.RetrievalTimeout, 100*time.Millisecond, 15*time.Second)
	providerTimeout, providerErr := duration("ANSWER_PROVIDER_TIMEOUT", defaults.ProviderTimeout, 100*time.Millisecond, 20*time.Second)
	configuration := Config{
		GRPCAddress: os.Getenv("ANSWER_GRPC_ADDR"), MetricsAddress: os.Getenv("ANSWER_METRICS_ADDR"), RetrievalAddress: os.Getenv("ANSWER_RETRIEVAL_GRPC_ADDR"),
		RetrievalDNSName: os.Getenv("ANSWER_RETRIEVAL_TLS_SERVER_NAME"), LLMBaseURL: os.Getenv("ANSWER_LLM_BASE_URL"), LLMModel: os.Getenv("ANSWER_LLM_MODEL"),
		LLMAPIKeyFile: os.Getenv("ANSWER_LLM_API_KEY_FILE"), LLMCAFile: os.Getenv("ANSWER_LLM_CA_FILE"),
		TLS:   internaltls.Files{CA: os.Getenv("ANSWER_TLS_CA_FILE"), Certificate: os.Getenv("ANSWER_TLS_CERT_FILE"), Key: os.Getenv("ANSWER_TLS_KEY_FILE")},
		RunAs: process.Identity{UID: uid, GID: gid}, Limits: application.Limits{MaximumEvidence: maximumEvidence, MaximumContextBytes: maximumContext,
			MaximumEvidenceBytes: maximumItem, MaximumSegments: maximumSegments, MaximumAnswerBytes: maximumAnswer, MaximumCitations: maximumCitations,
			MaximumOutputTokens: maximumTokens, ProviderConcurrency: concurrency, RequestTimeout: requestTimeout, RetrievalTimeout: retrievalTimeout, ProviderTimeout: providerTimeout},
	}
	if configuration.RetrievalDNSName == "" {
		configuration.RetrievalDNSName = "retrieval-service"
	}
	errs := []error{uidErr, gidErr, evidenceErr, contextErr, itemErr, segmentErr, answerErr, citationErr, tokenErr, concurrencyErr, requestErr, retrievalErr, providerErr}
	for _, err := range errs {
		if err != nil {
			return Config{}, errors.New("invalid answer configuration")
		}
	}
	if !validListenAddress(configuration.GRPCAddress) || !validListenAddress(configuration.MetricsAddress) || !validServiceAddress(configuration.RetrievalAddress) ||
		configuration.RetrievalDNSName != "retrieval-service" || !validProviderURL(configuration.LLMBaseURL) || strings.TrimSpace(configuration.LLMModel) == "" || len(configuration.LLMModel) > 256 || strings.ContainsAny(configuration.LLMModel, "\r\n") ||
		configuration.LLMAPIKeyFile == "" || configuration.TLS.CA == "" || configuration.TLS.Certificate == "" || configuration.TLS.Key == "" ||
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
