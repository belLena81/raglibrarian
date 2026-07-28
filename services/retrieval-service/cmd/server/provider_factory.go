package main

import (
	"errors"
	"net/http"

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
	retrievalSummaryProviderOpenAICompatible = "openai_compatible"
)

func configureEmbedder(configuration config.Config, httpClient *http.Client, serviceLogger *zap.Logger) (*embedding.TEI, error) {
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

func configureVectorStore(configuration config.Config, httpClient *http.Client) (*vector.Qdrant, error) {
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
		apiKey, err := provider.ReadAPIKey(configuration.SummaryLLMAPIKeyFile)
		if err != nil {
			if serviceLogger != nil {
				serviceLogger.Warn("retrieval summary provider disabled", zap.String("reason", "api_key_unavailable"))
			}
			return nil, nil
		}
		httpClient, err := provider.NewHTTPClient(configuration.SummaryLLMCAFile, configuration.SummaryLLMTimeout)
		if err != nil {
			if serviceLogger != nil {
				serviceLogger.Warn("retrieval summary provider disabled", zap.String("reason", "transport_unavailable"))
			}
			return nil, nil
		}
		limit, err := throttle.NewPerMinute(configuration.SummaryLLMRequestsPerMinute)
		if err != nil {
			if serviceLogger != nil {
				serviceLogger.Warn("retrieval summary provider disabled", zap.String("reason", "rate_limit_invalid"))
			}
			return nil, nil
		}
		outputMode, err := provider.ParseSummaryOutputMode(configuration.SummaryLLMOutputMode)
		if err != nil {
			if serviceLogger != nil {
				serviceLogger.Warn("retrieval summary provider disabled", zap.String("reason", "output_mode_invalid"))
			}
			return nil, nil
		}
		summaryProvider, err := provider.NewOpenAI(configuration.SummaryLLMBaseURL, configuration.SummaryLLMModel, apiKey, httpClient, serviceLogger, limit, configuration.SummaryLLMMaxOutputTokens, outputMode)
		if err != nil {
			if serviceLogger != nil {
				serviceLogger.Warn("retrieval summary provider disabled", zap.String("reason", "configuration_invalid"))
			}
			return nil, nil
		}
		return summaryProvider, nil
	default:
		if serviceLogger != nil {
			serviceLogger.Warn("retrieval summary provider disabled", zap.String("reason", "provider_unsupported"))
		}
		return nil, nil
	}
}
