package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/vector"
)

var ensureRetryDelay = time.Second

type initConfig struct {
	URL        string
	Collection string
	APIKeyFile string
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if err := run(ctx, os.Getenv, os.ReadFile, client); err != nil {
		log.Print("retrieval qdrant initializer failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, readFile func(string) ([]byte, error), client *http.Client) error {
	configuration, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	apiKey, err := readSecret(configuration.APIKeyFile, readFile)
	if err != nil {
		return err
	}
	store, err := vector.NewAuthenticatedQdrant(configuration.URL, configuration.Collection, apiKey, client)
	if err != nil {
		return err
	}
	return ensureCollection(ctx, store)
}

type collectionEnsurer interface {
	EnsureCollection(context.Context) error
}

func ensureCollection(ctx context.Context, store collectionEnsurer) error {
	var lastErr error
	for {
		if err := store.EnsureCollection(ctx); err != nil {
			lastErr = err
		} else {
			return nil
		}
		timer := time.NewTimer(ensureRetryDelay)
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
		collection = "evidence_v2"
	}
	configuration := initConfig{
		URL:        strings.TrimSpace(getenv("RETRIEVAL_QDRANT_URL")),
		Collection: collection,
		APIKeyFile: strings.TrimSpace(getenv("RETRIEVAL_QDRANT_API_KEY_FILE")),
	}
	if configuration.URL == "" || configuration.APIKeyFile == "" {
		return initConfig{}, errors.New("invalid qdrant initializer configuration")
	}
	return configuration, nil
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
