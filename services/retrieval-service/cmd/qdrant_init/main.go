package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/pkg/process"
	retrievalconfig "github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/vector"
)

const (
	defaultEnsureRetryDelay = time.Second
	defaultInitTimeout      = 20 * time.Second
	defaultHTTPTimeout      = 8 * time.Second
	maximumEnsureRetryDelay = time.Minute
	maximumInitTimeout      = 2 * time.Minute
	maximumHTTPTimeout      = time.Minute
)

type initConfig struct {
	URL         string
	Collection  string
	APIKeyFile  string
	RunAs       process.Identity
	RetryDelay  time.Duration
	InitTimeout time.Duration
	HTTPTimeout time.Duration
}

var dropPrivileges = process.DropPrivileges

func main() {
	configuration, err := loadConfig(os.Getenv)
	if err != nil {
		log.Printf("retrieval qdrant initializer failed: %v", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), configuration.InitTimeout)
	defer cancel()
	client := &http.Client{
		Timeout: configuration.HTTPTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if err = run(ctx, configuration, os.ReadFile, client); err != nil {
		log.Printf("retrieval qdrant initializer failed: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configuration initConfig, readFile func(string) ([]byte, error), client *http.Client) error {
	apiKey, err := readSecret(configuration.APIKeyFile, readFile)
	if err != nil {
		return err
	}
	if err = dropPrivileges(configuration.RunAs); err != nil {
		return err
	}
	minimumSearchScore, err := retrievalconfig.LoadMinimumSearchScore()
	if err != nil {
		return err
	}
	store, err := vector.NewAuthenticatedQdrant(configuration.URL, configuration.Collection, apiKey, client, minimumSearchScore)
	if err != nil {
		return err
	}
	return ensureCollection(ctx, store, configuration.RetryDelay)
}

type collectionEnsurer interface {
	EnsureCollection(context.Context) error
}

func ensureCollection(ctx context.Context, store collectionEnsurer, retryDelay time.Duration) error {
	var lastErr error
	for {
		if err := store.EnsureCollection(ctx); err != nil {
			lastErr = err
			if !errors.Is(err, vector.ErrVectorDependencyUnavailable) {
				return fmt.Errorf("ensure qdrant collection: %w", err)
			}
		} else {
			return nil
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("ensure qdrant collection: %w", errors.Join(lastErr, ctx.Err()))
		case <-timer.C:
		}
	}
}

func loadConfig(getenv func(string) string) (initConfig, error) {
	collection := strings.TrimSpace(getenv("RETRIEVAL_QDRANT_COLLECTION"))
	if collection == "" {
		collection = retrievalconfig.DefaultQdrantCollection
	}
	configuration := initConfig{
		URL:        strings.TrimSpace(getenv("RETRIEVAL_QDRANT_URL")),
		Collection: collection,
		APIKeyFile: strings.TrimSpace(getenv("RETRIEVAL_QDRANT_API_KEY_FILE")),
		RunAs: process.Identity{
			UID: positiveInteger(strings.TrimSpace(getenv("RUN_AS_UID")), 65532),
			GID: positiveInteger(strings.TrimSpace(getenv("RUN_AS_GID")), 65532),
		},
		RetryDelay:  positiveDuration(strings.TrimSpace(getenv("RETRIEVAL_QDRANT_RETRY_DELAY")), defaultEnsureRetryDelay, maximumEnsureRetryDelay),
		InitTimeout: positiveDuration(strings.TrimSpace(getenv("RETRIEVAL_QDRANT_INIT_TIMEOUT")), defaultInitTimeout, maximumInitTimeout),
		HTTPTimeout: positiveDuration(strings.TrimSpace(getenv("RETRIEVAL_QDRANT_HTTP_TIMEOUT")), defaultHTTPTimeout, maximumHTTPTimeout),
	}
	if configuration.URL == "" || configuration.APIKeyFile == "" || configuration.RunAs.UID < 1 || configuration.RunAs.GID < 1 ||
		configuration.RetryDelay <= 0 || configuration.InitTimeout <= 0 || configuration.HTTPTimeout <= 0 {
		return initConfig{}, errors.New("invalid qdrant initializer configuration")
	}
	return configuration, nil
}

func positiveInteger(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0
	}
	return parsed
}

func positiveDuration(value string, fallback, maximum time.Duration) time.Duration {
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 || parsed > maximum {
		return 0
	}
	return parsed
}

func readSecret(path string, readFile func(string) ([]byte, error)) (string, error) {
	if path == "" {
		return "", errors.New("secret path is required")
	}
	value, err := readFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimRight(string(value), "\r\n")
	if strings.TrimSpace(secret) == "" || strings.ContainsAny(secret, "\r\n") {
		return "", errors.New("secret file is invalid")
	}
	return secret, nil
}
