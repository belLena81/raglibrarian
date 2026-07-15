package config_test

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/identity-service/config"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("IDENTITY_POSTGRES_DSN", "postgres://fixture")
	t.Setenv("IDENTITY_SIGNING_KEY", hex.EncodeToString(make([]byte, 64)))
	t.Setenv("INTERNAL_TLS_CA_FILE", "/ca")
	t.Setenv("IDENTITY_TLS_CERT_FILE", "/cert")
	t.Setenv("IDENTITY_TLS_KEY_FILE", "/key")
}

func TestLoadParsesBoundedBcryptConfiguration(t *testing.T) {
	setRequired(t)
	t.Setenv("IDENTITY_BCRYPT_CONCURRENCY", "3")
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.BcryptConcurrency)
}

func TestLoadRejectsInvalidBcryptConfiguration(t *testing.T) {
	setRequired(t)
	t.Setenv("IDENTITY_BCRYPT_CONCURRENCY", "0")
	_, err := config.Load()
	assert.Error(t, err)
}
