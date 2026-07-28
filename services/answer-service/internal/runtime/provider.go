package runtime

import (
	"errors"
	"strings"

	"github.com/belLena81/raglibrarian/pkg/providerhttp"
	"github.com/belLena81/raglibrarian/services/answer-service/config"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/provider"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/throttle"
)

func NewGenerator(configuration config.GeneratorConfig) (application.AnswerGenerator, error) {
	apiKey, err := providerhttp.ReadSingleLineSecret(configuration.APIKeyFile, 4096)
	if err != nil {
		return nil, errors.New("load provider credentials")
	}
	httpClient, err := providerhttp.NewTLSHTTPClient(configuration.CAFile, configuration.HTTPClientTimeout)
	if err != nil {
		return nil, errors.New("configure provider transport")
	}
	limit, err := throttle.NewPerMinute(providerRequestsPerMinute(configuration.Model, configuration.RequestsPerMinute))
	if err != nil {
		return nil, errors.New("configure provider throttle")
	}
	providerAdapter, err := provider.NewOpenAIWithPolicy(configuration.BaseURL, configuration.Model, apiKey, httpClient, limit, configuration.LogErrorBody, provider.Policy{
		MaximumResponseBytes:  configuration.MaxResponseBytes,
		MaximumCandidateBytes: configuration.MaxCandidateBytes,
	})
	if err != nil {
		return nil, errors.New("configure openai compatible provider")
	}
	return providerAdapter, nil
}

func providerRequestsPerMinute(model string, configured int) int {
	if configured > 0 {
		return configured
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), ":free") {
		return 15
	}
	return configured
}
