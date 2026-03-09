package config_test

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/config"
)

// validKeyHex is a 32-byte all-zeros key encoded as 64 hex chars.
var validKeyHex = hex.EncodeToString(make([]byte, 32))

func setMinimalEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AUTH_SECRET_KEY", validKeyHex)
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/raglibrarian")
}

func TestLoad_ValidConfig_Succeeds(t *testing.T) {
	setMinimalEnv(t)

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Len(t, cfg.AuthSecretKey, 32)
	assert.Equal(t, "postgres://user:pass@localhost/raglibrarian", cfg.PostgresDSN)
	assert.Equal(t, ":8080", cfg.Addr)                // default
	assert.Equal(t, "24h0m0s", cfg.TokenTTL.String()) // default
}

func TestLoad_MissingAuthSecretKey_ReturnsError(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://x")
	t.Setenv("AUTH_SECRET_KEY", "")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "AUTH_SECRET_KEY")
}

func TestLoad_InvalidAuthSecretKey_TooShort(t *testing.T) {
	t.Setenv("AUTH_SECRET_KEY", "deadbeef") // only 4 bytes
	t.Setenv("POSTGRES_DSN", "postgres://x")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "AUTH_SECRET_KEY")
}

func TestLoad_InvalidAuthSecretKey_NotHex(t *testing.T) {
	t.Setenv("AUTH_SECRET_KEY", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
	t.Setenv("POSTGRES_DSN", "postgres://x")

	_, err := config.Load()

	require.Error(t, err)
}

func TestLoad_MissingPostgresDSN_ReturnsError(t *testing.T) {
	t.Setenv("AUTH_SECRET_KEY", validKeyHex)
	t.Setenv("POSTGRES_DSN", "")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "POSTGRES_DSN")
}

func TestLoad_CustomAddr(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv("QUERY_ADDR", ":9090")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.Addr)
}

func TestLoad_CustomTokenTTL_GoDuration(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv("TOKEN_TTL", "1h30m")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, "1h30m0s", cfg.TokenTTL.String())
}

func TestLoad_InvalidTokenTTL_ReturnsError(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv("TOKEN_TTL", "not-a-duration")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "TOKEN_TTL")
}
