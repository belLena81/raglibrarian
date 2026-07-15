package config_test

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/edge-api/config"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("EDGE_VERIFY_KEY", hex.EncodeToString(make([]byte, 32)))
	t.Setenv("INTERNAL_TLS_CA_FILE", "/ca")
	t.Setenv("EDGE_TLS_CERT_FILE", "/cert")
	t.Setenv("EDGE_TLS_KEY_FILE", "/key")
}

func TestLoadParsesExplicitSecurityConfiguration(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_TRUSTED_PROXY_CIDRS", "10.0.0.0/8")
	t.Setenv("EDGE_INSECURE_REFRESH_COOKIE", "true")
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.False(t, cfg.SecureCookie)
	require.Len(t, cfg.TrustedProxyCIDRs, 1)
	assert.Equal(t, 65532, cfg.RunAs.UID)
}

func TestLoadRejectsInvalidSecurityConfiguration(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_INSECURE_REFRESH_COOKIE", "sometimes")
	_, err := config.Load()
	assert.Error(t, err)
}
