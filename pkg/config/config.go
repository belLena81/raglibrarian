// Package config loads all service configuration from environment variables.
// It is the single place in the codebase that reads os.Getenv — no other
// package calls os.Getenv directly.
//
// Why a dedicated config package?
//   - Makes all tunable values visible in one file
//   - Validates at startup: missing required vars fail fast with a clear message
//   - Keeps infrastructure concerns (env reading, hex decoding) out of main()
//   - Makes config injectable in tests via Config struct literals
package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the query service.
// All fields are exported so tests can construct Config literals directly
// without touching the environment.
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

// Load reads all required and optional environment variables.
// Returns an error naming the first missing or invalid variable so operators
// get a precise message rather than a panic or a nil pointer.
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
	// Try Go duration syntax first (e.g. "24h", "30m").
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Fall back to interpreting a plain integer as seconds.
	secs, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %q as duration or seconds", s)
	}
	return time.Duration(secs) * time.Second, nil
}
