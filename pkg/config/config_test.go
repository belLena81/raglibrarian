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
	t.Setenv(config.EnvEdgeVerifyKey, validKeyHex)
}

func TestLoad_ValidConfig_Succeeds(t *testing.T) {
	setMinimalEnv(t)

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Len(t, cfg.VerifyKey, 32)
	assert.Equal(t, ":8080", cfg.Addr)
}

func TestLoad_MissingVerifyKey_ReturnsError(t *testing.T) {
	t.Setenv(config.EnvEdgeVerifyKey, "")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), config.EnvEdgeVerifyKey)
}

func TestLoad_InvalidVerifyKey_TooShort(t *testing.T) {
	t.Setenv(config.EnvEdgeVerifyKey, "deadbeef") // only 4 bytes

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), config.EnvEdgeVerifyKey)
}

func TestLoad_InvalidVerifyKey_NotHex(t *testing.T) {
	t.Setenv(config.EnvEdgeVerifyKey, "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")

	_, err := config.Load()

	require.Error(t, err)
}

func TestLoad_CustomAddr(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv(config.EnvQueryAddr, ":9090")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.Addr)
}

func TestLoad_ParsesTrustedProxyCIDRs(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv(config.EnvTrustedProxyCIDRs, "10.0.0.0/8, 2001:db8::/32")
	cfg, err := config.Load()
	require.NoError(t, err)
	require.Len(t, cfg.TrustedProxyCIDRs, 2)
	assert.Equal(t, "10.0.0.0/8", cfg.TrustedProxyCIDRs[0].String())
}

func TestLoad_RejectsInvalidTrustedProxyCIDR(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv(config.EnvTrustedProxyCIDRs, "not-a-cidr")
	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), config.EnvTrustedProxyCIDRs)
}
