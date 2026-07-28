package runtime

import (
	"errors"
	"net/http"
	"time"

	"github.com/belLena81/raglibrarian/pkg/providerhttp"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/embedding"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/provider"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/throttle"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/vector"
	"go.uber.org/zap"
)

func NewDependencyHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func NewEmbedder(configuration config.Config, httpClient *http.Client, serviceLogger *zap.Logger) (*embedding.TEI, error) {
	return newEmbedder(configuration.TEIURL, configuration.TEIRequestsPerSecond, embedding.RawResponseLog{
		Enabled:      configuration.TEILogRawResponse,
		MaximumBytes: configuration.TEILogRawResponseMaxBytes,
	}, embedding.Policy{
		MaximumResponseBytes: configuration.TEIMaxResponseBytes,
		ProviderBatchSize:    configuration.TEIBatchSize,
	}, httpClient, serviceLogger)
}

func NewWorkerEmbedder(configuration config.WorkerConfig, httpClient *http.Client, serviceLogger *zap.Logger) (*embedding.TEI, error) {
	return newEmbedder(configuration.TEIURL, configuration.TEIRequestsPerSecond, embedding.RawResponseLog{
		Enabled:      configuration.TEILogRawResponse,
		MaximumBytes: configuration.TEILogRawResponseMaxBytes,
	}, embedding.Policy{
		MaximumResponseBytes: configuration.TEIMaxResponseBytes,
		ProviderBatchSize:    configuration.TEIBatchSize,
	}, httpClient, serviceLogger)
}

func NewDirectEmbedder(endpoint string, requestsPerSecond int, rawResponseLog embedding.RawResponseLog, httpClient *http.Client, serviceLogger *zap.Logger) (*embedding.TEI, error) {
	return newEmbedder(endpoint, requestsPerSecond, rawResponseLog, embedding.Policy{
		MaximumResponseBytes: config.DefaultTEIMaxResponseBytes,
		ProviderBatchSize:    1,
	}, httpClient, serviceLogger)
}

func newEmbedder(endpoint string, requestsPerSecond int, rawResponseLog embedding.RawResponseLog, policy embedding.Policy, httpClient *http.Client, serviceLogger *zap.Logger) (*embedding.TEI, error) {
	limit, err := throttle.New(requestsPerSecond)
	if err != nil {
		return nil, errors.New("configure embedding throttle")
	}
	embedder, err := embedding.NewTEIWithOptions(endpoint, httpClient, serviceLogger, limit, rawResponseLog, policy)
	if err != nil {
		return nil, errors.New("configure tei embedding provider")
	}
	return embedder, nil
}

func NewVectorStore(configuration config.Config, httpClient *http.Client) (*vector.Qdrant, error) {
	apiKey, err := readAPIKey(configuration.QdrantAPIKeyFile)
	if err != nil {
		return nil, err
	}
	return newVectorStore(configuration.QdrantURL, configuration.QdrantCollection, apiKey, configuration.MinimumSearchScore, vector.Policy{
		MaximumResponseBytes:      configuration.QdrantMaxResponseBytes,
		MaximumBatchResponseBytes: configuration.QdrantBatchResponseBytes,
		DocumentEvidenceLimit:     configuration.QdrantDocumentEvidenceLimit,
	}, httpClient)
}

func NewWorkerVectorStore(configuration config.WorkerConfig, httpClient *http.Client) (*vector.Qdrant, error) {
	return newVectorStore(configuration.QdrantURL, configuration.QdrantCollection, configuration.QdrantAPIKey, configuration.MinimumSearchScore, vector.Policy{
		MaximumResponseBytes:      configuration.QdrantMaxResponseBytes,
		MaximumBatchResponseBytes: configuration.QdrantBatchResponseBytes,
		DocumentEvidenceLimit:     configuration.QdrantDocumentEvidenceLimit,
	}, httpClient)
}

func NewDirectVectorStore(endpoint, collection, apiKey string, minimumSearchScore float64, httpClient *http.Client) (*vector.Qdrant, error) {
	return newVectorStore(endpoint, collection, apiKey, minimumSearchScore, vector.Policy{
		MaximumResponseBytes:      config.DefaultQdrantMaxResponseBytes,
		MaximumBatchResponseBytes: config.DefaultQdrantBatchResponseBytes,
		DocumentEvidenceLimit:     config.DefaultQdrantDocumentEvidenceLimit,
	}, httpClient)
}

func newVectorStore(endpoint, collection, apiKey string, minimumSearchScore float64, policy vector.Policy, httpClient *http.Client) (*vector.Qdrant, error) {
	store, err := vector.NewAuthenticatedQdrantWithPolicy(endpoint, collection, apiKey, httpClient, minimumSearchScore, policy)
	if err != nil {
		return nil, errors.New("configure qdrant vector provider")
	}
	return store, nil
}

func NewEvidenceAssessor(configuration config.EvidenceAssessorConfig, serviceLogger *zap.Logger) (application.EvidenceAssessor, error) {
	if configuration.BaseURL == "" {
		return nil, nil
	}
	apiKey, err := providerhttp.ReadSingleLineSecret(configuration.APIKeyFile, 4096)
	if err != nil {
		return disableEvidenceAssessor(serviceLogger, "api_key_unavailable"), nil
	}
	httpClient, err := providerhttp.NewTLSHTTPClient(configuration.CAFile, configuration.Timeout)
	if err != nil {
		return disableEvidenceAssessor(serviceLogger, "transport_unavailable"), nil
	}
	limit, err := throttle.NewPerMinute(configuration.RequestsPerMinute)
	if err != nil {
		return disableEvidenceAssessor(serviceLogger, "rate_limit_invalid"), nil
	}
	outputMode, err := provider.ParseSummaryOutputMode(configuration.OutputMode)
	if err != nil {
		return disableEvidenceAssessor(serviceLogger, "output_mode_invalid"), nil
	}
	evidenceAssessor, err := provider.NewOpenAIWithOptions(configuration.BaseURL, configuration.Model, apiKey, httpClient, serviceLogger, limit, configuration.MaxOutputTokens, provider.Options{
		OutputMode: outputMode,
		Policy: provider.Policy{
			MaximumResponseBytes: configuration.MaxResponseBytes,
			MaximumSummaryBytes:  configuration.MaxSummaryBytes,
			MaximumInputRunes:    configuration.MaxInputRunes,
		},
	})
	if err != nil {
		return disableEvidenceAssessor(serviceLogger, "configuration_invalid"), nil
	}
	return evidenceAssessor, nil
}

func readAPIKey(path string) (string, error) {
	apiKey, err := providerhttp.ReadSingleLineSecret(path, 4096)
	if err != nil {
		return "", errors.New("read vector dependency credentials")
	}
	return apiKey, nil
}

func disableEvidenceAssessor(serviceLogger *zap.Logger, reason string) application.EvidenceAssessor {
	if serviceLogger != nil {
		serviceLogger.Warn("retrieval evidence assessor disabled", zap.String("reason", reason))
	}
	return nil
}
