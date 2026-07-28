package config_test

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	assert.Equal(t, "answer-service:50055", cfg.AnswerAddress)
	assert.Equal(t, 5*time.Minute, cfg.AnswerDeadline)
	assert.Equal(t, 2*time.Minute, cfg.RetrievalSearchDeadline)
	assert.Equal(t, 6*time.Second, cfg.CatalogPreviewDeadline)
	assert.Equal(t, 10, cfg.AnswerRateLimit)
	assert.Equal(t, 3*time.Minute, cfg.AnswerRateWindow)
	assert.True(t, cfg.RetrievalReadinessRequired)
	assert.Equal(t, 0.6, cfg.MinimumEvidenceScore)
	assert.Equal(t, 30, cfg.QueryRateLimit)
	assert.Equal(t, time.Minute, cfg.QueryRateWindow)
	assert.Equal(t, 10000, cfg.QueryRateMaxKeys)
	assert.Equal(t, 8, cfg.QueryConcurrency)
	assert.Equal(t, 5, cfg.SetupAdminRateLimit)
	assert.Equal(t, 15*time.Minute, cfg.SetupAdminRateWindow)
	assert.Equal(t, 1000, cfg.SetupAdminRateMaxKeys)
	assert.Equal(t, 20, cfg.BookUploadRateLimit)
	assert.Equal(t, time.Hour, cfg.BookUploadRateWindow)
	assert.Equal(t, 10000, cfg.BookUploadRateMaxKeys)
	assert.Equal(t, 2*time.Minute+10*time.Second, cfg.BookUploadDeadline)
}

func TestLoadParsesRetrievalReadinessPolicy(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_RETRIEVAL_READINESS_REQUIRED", "false")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.False(t, cfg.RetrievalReadinessRequired)
}

func TestLoadParsesQueryAdmissionControls(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_QUERY_RATE_LIMIT", "12")
	t.Setenv("EDGE_QUERY_RATE_WINDOW", "30s")
	t.Setenv("EDGE_QUERY_RATE_MAX_KEYS", "500")
	t.Setenv("EDGE_QUERY_CONCURRENCY", "3")
	t.Setenv("EDGE_SETUP_ADMIN_RATE_LIMIT", "7")
	t.Setenv("EDGE_SETUP_ADMIN_RATE_WINDOW", "20m")
	t.Setenv("EDGE_SETUP_ADMIN_RATE_MAX_KEYS", "700")
	t.Setenv("EDGE_BOOK_UPLOAD_RATE_LIMIT", "40")
	t.Setenv("EDGE_BOOK_UPLOAD_RATE_WINDOW", "15m")
	t.Setenv("EDGE_BOOK_UPLOAD_RATE_MAX_KEYS", "600")
	t.Setenv("EDGE_BOOK_UPLOAD_DEADLINE", "90s")
	t.Setenv("EDGE_ANSWER_DEADLINE", "7s")
	t.Setenv("EDGE_RETRIEVAL_SEARCH_DEADLINE", "11s")
	t.Setenv("EDGE_CATALOG_PREVIEW_DEADLINE", "9s")
	t.Setenv("EDGE_ANSWER_RATE_LIMIT", "9")
	t.Setenv("EDGE_ANSWER_RATE_WINDOW", "45s")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, 12, cfg.QueryRateLimit)
	assert.Equal(t, 30*time.Second, cfg.QueryRateWindow)
	assert.Equal(t, 500, cfg.QueryRateMaxKeys)
	assert.Equal(t, 3, cfg.QueryConcurrency)
	assert.Equal(t, 7, cfg.SetupAdminRateLimit)
	assert.Equal(t, 20*time.Minute, cfg.SetupAdminRateWindow)
	assert.Equal(t, 700, cfg.SetupAdminRateMaxKeys)
	assert.Equal(t, 40, cfg.BookUploadRateLimit)
	assert.Equal(t, 15*time.Minute, cfg.BookUploadRateWindow)
	assert.Equal(t, 600, cfg.BookUploadRateMaxKeys)
	assert.Equal(t, 90*time.Second, cfg.BookUploadDeadline)
	assert.Equal(t, 7*time.Second, cfg.AnswerDeadline)
	assert.Equal(t, 11*time.Second, cfg.RetrievalSearchDeadline)
	assert.Equal(t, 9*time.Second, cfg.CatalogPreviewDeadline)
	assert.Equal(t, 9, cfg.AnswerRateLimit)
	assert.Equal(t, 45*time.Second, cfg.AnswerRateWindow)
}

func TestLoadParsesMinimumEvidenceScore(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_MINIMUM_EVIDENCE_SCORE", "0.75")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, 0.75, cfg.MinimumEvidenceScore)
}

func TestLoadRejectsInvalidMinimumEvidenceScore(t *testing.T) {
	for _, value := range []string{"0", "NaN", "+Inf"} {
		setRequired(t)
		t.Setenv("EDGE_MINIMUM_EVIDENCE_SCORE", value)
		_, err := config.Load()
		require.Error(t, err, "value %q should be rejected", value)
		assert.ErrorIs(t, err, config.ErrQueryLimitConfiguration)
	}
}

func TestLoadAcceptsMaximumAnswerDeadline(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_ANSWER_DEADLINE", "5m")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, cfg.AnswerDeadline)
}

func TestLoadAcceptsMaximumCatalogPreviewDeadline(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_CATALOG_PREVIEW_DEADLINE", "30s")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, cfg.CatalogPreviewDeadline)
}

func TestLoadRejectsCatalogPreviewDeadlineAtOrAboveRetrievalSearchDeadline(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_RETRIEVAL_SEARCH_DEADLINE", "9s")
	t.Setenv("EDGE_CATALOG_PREVIEW_DEADLINE", "9s")

	_, err := config.Load()

	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrQueryLimitConfiguration)
}

func TestLoadRejectsOversizedAnswerDeadline(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_ANSWER_DEADLINE", "5m1s")

	_, err := config.Load()

	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrQueryLimitConfiguration)
}

func TestLoadRejectsOversizedCatalogPreviewDeadline(t *testing.T) {
	setRequired(t)
	t.Setenv("EDGE_CATALOG_PREVIEW_DEADLINE", "31s")

	_, err := config.Load()

	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrQueryLimitConfiguration)
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
			name: "query rate invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_QUERY_RATE_LIMIT", "0")
			},
			expected: config.ErrQueryLimitConfiguration,
		},
		{
			name: "setup admin rate invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_SETUP_ADMIN_RATE_LIMIT", "0")
			},
			expected: config.ErrQueryLimitConfiguration,
		},
		{
			name: "setup admin window invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_SETUP_ADMIN_RATE_WINDOW", "0s")
			},
			expected: config.ErrQueryLimitConfiguration,
		},
		{
			name: "setup admin max keys invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_SETUP_ADMIN_RATE_MAX_KEYS", "0")
			},
			expected: config.ErrQueryLimitConfiguration,
		},
		{
			name: "book upload rate invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_BOOK_UPLOAD_RATE_LIMIT", "0")
			},
			expected: config.ErrQueryLimitConfiguration,
		},
		{
			name: "book upload window invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_BOOK_UPLOAD_RATE_WINDOW", "0s")
			},
			expected: config.ErrQueryLimitConfiguration,
		},
		{
			name: "book upload max keys invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_BOOK_UPLOAD_RATE_MAX_KEYS", "0")
			},
			expected: config.ErrQueryLimitConfiguration,
		},
		{
			name: "book upload deadline invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_BOOK_UPLOAD_DEADLINE", "0s")
			},
			expected: config.ErrQueryLimitConfiguration,
		},
		{
			name: "answer rate invalid",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_ANSWER_RATE_LIMIT", "0")
			},
			expected: config.ErrQueryLimitConfiguration,
		},
		{
			name: "answer deadline exceeds bound",
			configure: func(t *testing.T) {
				t.Setenv("EDGE_ANSWER_DEADLINE", "6m")
			},
			expected: config.ErrQueryLimitConfiguration,
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
