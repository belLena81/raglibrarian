package runtime

import (
	"errors"

	"github.com/belLena81/raglibrarian/pkg/providerhttp"
	"github.com/belLena81/raglibrarian/services/answer-service/config"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/provider"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/throttle"
)

func NewLLMProvider(configuration config.Config) (application.LLMProvider, error) {
	apiKey, err := providerhttp.ReadSingleLineSecret(configuration.LLMAPIKeyFile, 4096)
	if err != nil {
		return nil, errors.New("load provider credentials")
	}
	httpClient, err := providerhttp.NewTLSHTTPClient(configuration.LLMCAFile, configuration.LLMHTTPClientTimeout)
	if err != nil {
		return nil, errors.New("configure provider transport")
	}
	limit, err := throttle.NewPerMinute(configuration.LLMRequestsPerMinute)
	if err != nil {
		return nil, errors.New("configure provider throttle")
	}
	providerAdapter, err := provider.NewOpenAIWithPolicy(configuration.LLMBaseURL, configuration.LLMModel, apiKey, httpClient, limit, configuration.LogProviderErrorBody, provider.Policy{
		MaximumResponseBytes:  configuration.LLMMaxResponseBytes,
		MaximumCandidateBytes: configuration.LLMMaxCandidateBytes,
	})
	if err != nil {
		return nil, errors.New("configure openai compatible provider")
	}
	return providerAdapter, nil
}
