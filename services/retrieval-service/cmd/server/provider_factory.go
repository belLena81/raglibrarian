package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/belLena81/raglibrarian/pkg/providerhttp"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/embedding"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/provider"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/throttle"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/vector"
	"go.uber.org/zap"
)

const (
	retrievalEmbeddingProviderTEI            = "tei"
	retrievalVectorProviderQdrant            = "qdrant"
	retrievalSummaryProviderOpenAICompatible = providerhttp.OpenAICompatibleProviderKind
)

type queryEmbedder interface {
	application.QueryEmbedder
	CheckReady(context.Context) error
}

type evidenceStore interface {
	application.EvidenceStore
	CheckReady(context.Context) error
}

func configureEmbedder(configuration config.Config, httpClient *http.Client, serviceLogger *zap.Logger) (queryEmbedder, error) {
	switch configuration.EmbeddingProviderKind {
	case retrievalEmbeddingProviderTEI:
		teiLimiter, err := throttle.New(configuration.TEIRequestsPerSecond)
		if err != nil {
			return nil, errors.New("configure embedding throttle")
		}
		embedder, err := embedding.NewTEIWithOptions(configuration.TEIURL, httpClient, serviceLogger, teiLimiter, embedding.RawResponseLog{
			Enabled:      configuration.TEILogRawResponse,
			MaximumBytes: configuration.TEILogRawResponseMaxBytes,
		})
		if err != nil {
			return nil, errors.New("configure tei embedding provider")
		}
		return embedder, nil
	default:
		return nil, errors.New("unsupported embedding provider kind")
	}
}

func configureVectorStore(configuration config.Config, httpClient *http.Client) (evidenceStore, error) {
	switch configuration.VectorProviderKind {
	case retrievalVectorProviderQdrant:
		apiKey, err := readSecret(configuration.QdrantAPIKeyFile)
		if err != nil {
			return nil, errors.New("read vector dependency credentials")
		}
		store, err := vector.NewAuthenticatedQdrant(configuration.QdrantURL, configuration.QdrantCollection, apiKey, httpClient, configuration.MinimumSearchScore)
		if err != nil {
			return nil, errors.New("configure qdrant vector provider")
		}
		return store, nil
	default:
		return nil, errors.New("unsupported vector provider kind")
	}
}

func configureSummaryProvider(configuration config.Config, serviceLogger *zap.Logger) (application.SummaryProvider, error) {
	if configuration.SummaryLLMBaseURL == "" {
		return nil, nil
	}
	switch configuration.SummaryLLMProviderKind {
	case retrievalSummaryProviderOpenAICompatible:
		apiKey, err := providerhttp.ReadSingleLineSecret(configuration.SummaryLLMAPIKeyFile, 4096)
		if err != nil {
			return disableSummaryProvider(serviceLogger, "api_key_unavailable"), nil
		}
		httpClient, err := providerhttp.NewTLSHTTPClient(configuration.SummaryLLMCAFile, configuration.SummaryLLMTimeout)
		if err != nil {
			return disableSummaryProvider(serviceLogger, "transport_unavailable"), nil
		}
		limit, err := throttle.NewPerMinute(configuration.SummaryLLMRequestsPerMinute)
		if err != nil {
			return disableSummaryProvider(serviceLogger, "rate_limit_invalid"), nil
		}
		outputMode, err := provider.ParseSummaryOutputMode(configuration.SummaryLLMOutputMode)
		if err != nil {
			return disableSummaryProvider(serviceLogger, "output_mode_invalid"), nil
		}
		summaryProvider, err := provider.NewOpenAI(configuration.SummaryLLMBaseURL, configuration.SummaryLLMModel, apiKey, httpClient, serviceLogger, limit, configuration.SummaryLLMMaxOutputTokens, outputMode)
		if err != nil {
			return disableSummaryProvider(serviceLogger, "configuration_invalid"), nil
		}
		return summaryProvider, nil
	default:
		return disableSummaryProvider(serviceLogger, "provider_unsupported"), nil
	}
}

func disableSummaryProvider(serviceLogger *zap.Logger, reason string) application.SummaryProvider {
	if serviceLogger != nil {
		serviceLogger.Warn("retrieval summary provider disabled", zap.String("reason", reason))
	}
	return nil
}
