// Package config loads service configuration from environment variables.
package config

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// Environment variable names read by Load.
const (
	EnvEdgeVerifyKey = "EDGE_VERIFY_KEY"
	EnvQueryAddr     = "QUERY_ADDR"
	EnvLogEnv        = "LOG_ENV"
	EnvLogLevel      = "LOG_LEVEL"
)

// Config holds all runtime configuration for the query service.
type Config struct {
	// HTTP
	Addr string // e.g. ":8080"

	// Auth
	VerifyKey []byte // 32-byte Ed25519 public key for PASETO verification

	// Logging
	LogEnv   string // "production" | "" (development)
	LogLevel string // "debug" | "info" | "warn" | "error"
}

// Load reads and validates all required and optional environment variables.
func Load() (Config, error) {
	keyHex, err := requireEnv(EnvEdgeVerifyKey)
	if err != nil {
		return Config{}, err
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(keyHex) != 64 || len(key) != 32 {
		return Config{}, fmt.Errorf("%w: %s must be 64 hex chars", domain.ErrInvalidSecretKey, EnvEdgeVerifyKey)
	}

	return Config{
		Addr:      optionalEnv(EnvQueryAddr, ":8080"),
		VerifyKey: key,
		LogEnv:    optionalEnv(EnvLogEnv, ""),
		LogLevel:  optionalEnv(EnvLogLevel, ""),
	}, nil
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return v, fmt.Errorf("%w: %s", domain.ErrMissingEnvVar, key)
	}
	return v, nil
}

func optionalEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
