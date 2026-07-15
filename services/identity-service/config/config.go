// Package config loads and validates Identity runtime configuration.
package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
)

// Config is validated Identity runtime configuration.
type Config struct {
	Address, DSN      string
	SigningKey        []byte
	BcryptConcurrency int
	TLS               internaltls.Files
	RunAs             process.Identity
}

// Load reads Identity configuration from the environment.
func Load() (Config, error) {
	dsn, err := required("IDENTITY_POSTGRES_DSN")
	if err != nil {
		return Config{}, err
	}
	keyHex, err := required("IDENTITY_SIGNING_KEY")
	if err != nil {
		return Config{}, err
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 64 {
		return Config{}, fmt.Errorf("IDENTITY_SIGNING_KEY must be 128 hex characters")
	}
	concurrency, err := strconv.Atoi(optional("IDENTITY_BCRYPT_CONCURRENCY", "4"))
	if err != nil || concurrency < 1 {
		return Config{}, fmt.Errorf("IDENTITY_BCRYPT_CONCURRENCY must be a positive integer")
	}
	uid, err := strconv.Atoi(optional("RUN_AS_UID", "65532"))
	if err != nil {
		return Config{}, fmt.Errorf("RUN_AS_UID: %w", err)
	}
	gid, err := strconv.Atoi(optional("RUN_AS_GID", "65532"))
	if err != nil {
		return Config{}, fmt.Errorf("RUN_AS_GID: %w", err)
	}
	if uid < 1 || gid < 1 {
		return Config{}, fmt.Errorf("RUN_AS_UID and RUN_AS_GID must be positive")
	}
	ca, err := required("INTERNAL_TLS_CA_FILE")
	if err != nil {
		return Config{}, err
	}
	cert, err := required("IDENTITY_TLS_CERT_FILE")
	if err != nil {
		return Config{}, err
	}
	keyFile, err := required("IDENTITY_TLS_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	return Config{Address: optional("IDENTITY_GRPC_ADDR", ":50051"), DSN: dsn, SigningKey: key, BcryptConcurrency: concurrency, TLS: internaltls.Files{CA: ca, Certificate: cert, Key: keyFile}, RunAs: process.Identity{UID: uid, GID: gid}}, nil
}

func required(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}
func optional(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
