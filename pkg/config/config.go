// Package config loads service configuration from environment variables.
package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration for the query service.
type Config struct {
	// HTTP
	Addr string // e.g. ":8080"

	// Auth
	AuthSecretKey []byte        // 32-byte symmetric key for PASETO
	TokenTTL      time.Duration // lifetime of issued tokens

	// Postgres
	PostgresDSN string

	// Logging
	LogEnv   string // "production" | "" (development)
	LogLevel string // "debug" | "info" | "warn" | "error"
}

// Load reads and validates all required and optional environment variables.
func Load() (Config, error) {
	keyHex, err := requireEnv("AUTH_SECRET_KEY")
	if err != nil {
		return Config{}, err
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return Config{}, fmt.Errorf(
			"config: AUTH_SECRET_KEY must be exactly 64 hex characters (got %d chars)", len(keyHex),
		)
	}

	dsn, err := requireEnv("POSTGRES_DSN")
	if err != nil {
		return Config{}, err
	}

	ttl, err := parseDuration(optionalEnv("TOKEN_TTL", "24h"))
	if err != nil {
		return Config{}, fmt.Errorf("config: invalid TOKEN_TTL: %w", err)
	}

	return Config{
		Addr:          optionalEnv("QUERY_ADDR", ":8080"),
		AuthSecretKey: key,
		TokenTTL:      ttl,
		PostgresDSN:   dsn,
		LogEnv:        optionalEnv("LOG_ENV", ""),
		LogLevel:      optionalEnv("LOG_LEVEL", ""),
	}, nil
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("config: required environment variable %q is not set", key)
	}
	return v, nil
}

func optionalEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	secs, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %q as duration or seconds", s)
	}
	return time.Duration(secs) * time.Second, nil
}
