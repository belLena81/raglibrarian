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
	t.Setenv(config.EnvAuthSecretKey, validKeyHex)
}

func TestLoad_ValidConfig_Succeeds(t *testing.T) {
	setMinimalEnv(t)

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Len(t, cfg.AuthSecretKey, 32)
	assert.Equal(t, ":8080", cfg.Addr)
	assert.Empty(t, cfg.PostgresDSN)
	assert.Zero(t, cfg.TokenTTL)
}

func TestLoad_MissingAuthSecretKey_ReturnsError(t *testing.T) {
	t.Setenv(config.EnvAuthSecretKey, "")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), config.EnvAuthSecretKey)
}

func TestLoad_InvalidAuthSecretKey_TooShort(t *testing.T) {
	t.Setenv(config.EnvAuthSecretKey, "deadbeef") // only 4 bytes

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), config.EnvAuthSecretKey)
}

func TestLoad_InvalidAuthSecretKey_NotHex(t *testing.T) {
	t.Setenv(config.EnvAuthSecretKey, "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")

	_, err := config.Load()

	require.Error(t, err)
}

func TestLoad_MissingPostgresDSN_IsAllowedForEdge(t *testing.T) {
	t.Setenv(config.EnvAuthSecretKey, validKeyHex)
	t.Setenv(config.EnvPostgresDSN, "")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Empty(t, cfg.PostgresDSN)
}

func TestLoad_CustomAddr(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv(config.EnvQueryAddr, ":9090")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.Addr)
}

func TestLoad_TokenTTLIsIgnoredByEdge(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv(config.EnvTokenTTL, "1h30m")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Zero(t, cfg.TokenTTL)
}

func TestLoad_InvalidTokenTTLIsIgnoredByEdge(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv(config.EnvTokenTTL, "not-a-duration")

	_, err := config.Load()

	require.NoError(t, err)
}
