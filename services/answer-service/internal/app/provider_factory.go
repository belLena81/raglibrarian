package app

import (
	"errors"
	"time"

	"github.com/belLena81/raglibrarian/pkg/providerhttp"
	"github.com/belLena81/raglibrarian/services/answer-service/config"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/provider"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/throttle"
)

func configureLLMProvider(configuration config.Config) (application.LLMProvider, error) {
	switch configuration.LLMProviderKind {
	case providerhttp.OpenAICompatibleProviderKind:
		apiKey, err := providerhttp.ReadSingleLineSecret(configuration.LLMAPIKeyFile, 4096)
		if err != nil {
			return nil, errors.New("load provider credentials")
		}
		httpClient, err := providerhttp.NewTLSHTTPClient(configuration.LLMCAFile, 0*time.Second)
		if err != nil {
			return nil, errors.New("configure provider transport")
		}
		limit, err := throttle.NewPerMinute(configuration.LLMRequestsPerMinute)
		if err != nil {
			return nil, errors.New("configure provider throttle")
		}
		providerAdapter, err := provider.NewOpenAI(configuration.LLMBaseURL, configuration.LLMModel, apiKey, httpClient, limit, configuration.LogProviderErrorBody)
		if err != nil {
			return nil, errors.New("configure openai compatible provider")
		}
		return providerAdapter, nil
	default:
		return nil, errors.New("unsupported provider kind")
	}
}
