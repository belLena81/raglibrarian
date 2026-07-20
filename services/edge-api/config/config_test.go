package config_test

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/edge-api/config"
)

func setRequired(t *testing.T) {
	t.Helper()
	secretPath := filepath.Join(t.TempDir(), "rabbit-uri")
	require.NoError(t, os.WriteFile(secretPath, []byte("amqp://edge-status:test@rabbitmq:5672/"), 0o600))
	t.Setenv("EDGE_STATUS_RABBITMQ_URI_FILE", secretPath)
	t.Setenv("EDGE_VERIFY_KEY", hex.EncodeToString(make([]byte, 32)))
	t.Setenv("EDGE_TRUSTED_PROXY_CIDRS", "")
	t.Setenv("EDGE_INSECURE_REFRESH_COOKIE", "false")
	t.Setenv("RUN_AS_UID", "65532")
	t.Setenv("RUN_AS_GID", "65532")
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
	assert.Equal(t, "retrieval-service:50054", cfg.RetrievalAddress)
	assert.True(t, cfg.RetrievalReadinessRequired)
}

func TestLoadParsesRetrievalReadinessPolicy(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_RETRIEVAL_READINESS_REQUIRED", "false")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.False(t, cfg.RetrievalReadinessRequired)
}

func TestLoadRejectsInvalidSecurityConfiguration(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_INSECURE_REFRESH_COOKIE", "sometimes")
	_, err := config.Load()
	assert.ErrorIs(t, err, config.ErrRefreshCookieConfiguration)
}

func TestLoadClassifiesConfigurationFailures(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T)
		expected  error
	}{
		{
			name: "required value missing",
			configure: func(t *testing.T) {
				t.Setenv("INTERNAL_TLS_CA_FILE", "")
			},
			expected: config.ErrRequiredConfiguration,
		},
		{
			name: "verify key invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_VERIFY_KEY", "not-a-key")
			},
			expected: config.ErrVerifyKeyConfiguration,
		},
		{
			name: "trusted proxy CIDR invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_TRUSTED_PROXY_CIDRS", "not-a-cidr")
			},
			expected: config.ErrTrustedProxyConfiguration,
		},
		{
			name: "refresh cookie policy invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_INSECURE_REFRESH_COOKIE", "sometimes")
			},
			expected: config.ErrRefreshCookieConfiguration,
		},
		{
			name: "retrieval readiness policy invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_RETRIEVAL_READINESS_REQUIRED", "sometimes")
			},
			expected: nil,
		},
		{
			name: "run identity invalid",
			configure: func(t *testing.T) {
				t.Setenv("RUN_AS_UID", "root")
			},
			expected: config.ErrRunIdentityConfiguration,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setRequired(t)
			test.configure(t)

			_, err := config.Load()

			require.Error(t, err)
			if test.expected != nil {
				assert.True(t, errors.Is(err, test.expected))
			} else {
				assert.Contains(t, err.Error(), "EDGE_RETRIEVAL_READINESS_REQUIRED")
			}
		})
	}
}
