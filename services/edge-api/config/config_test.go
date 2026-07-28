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
	assert.Equal(t, 30*24*time.Hour, cfg.RefreshCookieMaxAge)
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
	assert.Equal(t, 20, cfg.AuthRegisterRateLimit)
	assert.Equal(t, time.Hour, cfg.AuthRegisterRateWindow)
	assert.Equal(t, 10000, cfg.AuthRegisterRateMaxKeys)
	assert.Equal(t, 30, cfg.AuthVerifyEmailRateLimit)
	assert.Equal(t, time.Hour, cfg.AuthVerifyEmailRateWindow)
	assert.Equal(t, 10000, cfg.AuthVerifyEmailRateMaxKeys)
	assert.Equal(t, 30, cfg.AuthLoginRateLimit)
	assert.Equal(t, time.Minute, cfg.AuthLoginRateWindow)
	assert.Equal(t, 10000, cfg.AuthLoginRateMaxKeys)
	assert.Equal(t, 5, cfg.AuthResendVerificationRateLimit)
	assert.Equal(t, time.Hour, cfg.AuthResendVerificationRateWindow)
	assert.Equal(t, 10000, cfg.AuthResendVerificationRateMaxKeys)
	assert.Equal(t, 5, cfg.AuthPasswordResetRequestRateLimit)
	assert.Equal(t, time.Hour, cfg.AuthPasswordResetRequestRateWindow)
	assert.Equal(t, 10000, cfg.AuthPasswordResetRequestRateMaxKeys)
	assert.Equal(t, 5, cfg.AuthPasswordResetVerifyRateLimit)
	assert.Equal(t, time.Hour, cfg.AuthPasswordResetVerifyRateWindow)
	assert.Equal(t, 10000, cfg.AuthPasswordResetVerifyRateMaxKeys)
	assert.Equal(t, 5, cfg.AuthPasswordResetCompleteRateLimit)
	assert.Equal(t, time.Hour, cfg.AuthPasswordResetCompleteRateWindow)
	assert.Equal(t, 10000, cfg.AuthPasswordResetCompleteRateMaxKeys)
	assert.Equal(t, 5, cfg.SetupAdminRateLimit)
	assert.Equal(t, 15*time.Minute, cfg.SetupAdminRateWindow)
	assert.Equal(t, 1000, cfg.SetupAdminRateMaxKeys)
	assert.Equal(t, 20, cfg.BookUploadRateLimit)
	assert.Equal(t, time.Hour, cfg.BookUploadRateWindow)
	assert.Equal(t, 10000, cfg.BookUploadRateMaxKeys)
	assert.Equal(t, 2*time.Minute+10*time.Second, cfg.BookUploadDeadline)
	assert.Equal(t, 10*time.Second, cfg.HTTPReadTimeout)
	assert.Equal(t, 5*time.Second, cfg.HTTPReadHeaderTimeout)
	assert.Equal(t, 5*time.Second, cfg.HTTPWriteTimeoutHeadroom)
	assert.Equal(t, 30*time.Second, cfg.HTTPMinimumWriteTimeout)
	assert.Equal(t, time.Minute, cfg.HTTPIdleTimeout)
	assert.Equal(t, 15*time.Second, cfg.HTTPShutdownTimeout)
	assert.Equal(t, 1<<20, cfg.HTTPMaxHeaderBytes)
	assert.Equal(t, 3*time.Second, cfg.IdentityRPCDeadline)
	assert.Equal(t, 2*time.Second, cfg.CatalogReadinessTimeout)
	assert.Equal(t, 2*time.Minute, cfg.CatalogUploadTimeout)
	assert.Equal(t, 3*time.Second, cfg.CatalogListTimeout)
	assert.Equal(t, 2*time.Second, cfg.RetrievalReadinessTimeout)
	assert.Equal(t, 6*time.Second, cfg.BooksListTimeout)
	assert.Equal(t, 5*time.Second, cfg.BooksLifecycleTimeout)
	assert.Equal(t, 15*time.Second, cfg.SSEHeartbeatInterval)
	assert.Equal(t, 15*time.Second, cfg.SSERevalidateInterval)
	assert.Equal(t, 5*time.Minute, cfg.SSEMaximumDuration)
	assert.Equal(t, 5*time.Second, cfg.SSEWriteTimeout)
	assert.Equal(t, time.Second, cfg.PendingWatchReconnectInitialBackoff)
	assert.Equal(t, 30*time.Second, cfg.PendingWatchReconnectMaxBackoff)
	assert.Equal(t, time.Second, cfg.BookStatusReconnectInitialBackoff)
	assert.Equal(t, 30*time.Second, cfg.BookStatusReconnectMaxBackoff)
	assert.Equal(t, 5*time.Second, cfg.BookStatusDialTimeout)
	assert.Equal(t, 10*time.Second, cfg.BookStatusHeartbeatTimeout)
	assert.Equal(t, 20, cfg.BookStatusPrefetch)
	assert.Equal(t, 64<<20, cfg.BookStatusQueueMaxLengthBytes)
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
	t.Setenv("EDGE_AUTH_REGISTER_RATE_LIMIT", "21")
	t.Setenv("EDGE_AUTH_REGISTER_RATE_WINDOW", "2h")
	t.Setenv("EDGE_AUTH_REGISTER_RATE_MAX_KEYS", "111")
	t.Setenv("EDGE_AUTH_VERIFY_EMAIL_RATE_LIMIT", "31")
	t.Setenv("EDGE_AUTH_VERIFY_EMAIL_RATE_WINDOW", "70m")
	t.Setenv("EDGE_AUTH_VERIFY_EMAIL_RATE_MAX_KEYS", "222")
	t.Setenv("EDGE_AUTH_LOGIN_RATE_LIMIT", "32")
	t.Setenv("EDGE_AUTH_LOGIN_RATE_WINDOW", "70s")
	t.Setenv("EDGE_AUTH_LOGIN_RATE_MAX_KEYS", "333")
	t.Setenv("EDGE_AUTH_RESEND_VERIFICATION_RATE_LIMIT", "6")
	t.Setenv("EDGE_AUTH_RESEND_VERIFICATION_RATE_WINDOW", "61m")
	t.Setenv("EDGE_AUTH_RESEND_VERIFICATION_RATE_MAX_KEYS", "444")
	t.Setenv("EDGE_AUTH_PASSWORD_RESET_REQUEST_RATE_LIMIT", "7")
	t.Setenv("EDGE_AUTH_PASSWORD_RESET_REQUEST_RATE_WINDOW", "62m")
	t.Setenv("EDGE_AUTH_PASSWORD_RESET_REQUEST_RATE_MAX_KEYS", "555")
	t.Setenv("EDGE_AUTH_PASSWORD_RESET_VERIFY_RATE_LIMIT", "8")
	t.Setenv("EDGE_AUTH_PASSWORD_RESET_VERIFY_RATE_WINDOW", "63m")
	t.Setenv("EDGE_AUTH_PASSWORD_RESET_VERIFY_RATE_MAX_KEYS", "666")
	t.Setenv("EDGE_AUTH_PASSWORD_RESET_COMPLETE_RATE_LIMIT", "9")
	t.Setenv("EDGE_AUTH_PASSWORD_RESET_COMPLETE_RATE_WINDOW", "64m")
	t.Setenv("EDGE_AUTH_PASSWORD_RESET_COMPLETE_RATE_MAX_KEYS", "777")
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
	t.Setenv("EDGE_HTTP_READ_TIMEOUT", "11s")
	t.Setenv("EDGE_HTTP_READ_HEADER_TIMEOUT", "6s")
	t.Setenv("EDGE_HTTP_WRITE_TIMEOUT_HEADROOM", "7s")
	t.Setenv("EDGE_HTTP_MINIMUM_WRITE_TIMEOUT", "31s")
	t.Setenv("EDGE_HTTP_IDLE_TIMEOUT", "61s")
	t.Setenv("EDGE_HTTP_SHUTDOWN_TIMEOUT", "16s")
	t.Setenv("EDGE_HTTP_MAX_HEADER_BYTES", "2048")
	t.Setenv("EDGE_IDENTITY_RPC_DEADLINE", "4s")
	t.Setenv("EDGE_CATALOG_READINESS_TIMEOUT", "2500ms")
	t.Setenv("EDGE_CATALOG_UPLOAD_TIMEOUT", "75s")
	t.Setenv("EDGE_CATALOG_LIST_TIMEOUT", "4s")
	t.Setenv("EDGE_RETRIEVAL_READINESS_TIMEOUT", "2500ms")
	t.Setenv("EDGE_BOOKS_LIST_TIMEOUT", "7s")
	t.Setenv("EDGE_BOOKS_LIFECYCLE_TIMEOUT", "8s")
	t.Setenv("EDGE_SSE_HEARTBEAT_INTERVAL", "12s")
	t.Setenv("EDGE_SSE_REVALIDATE_INTERVAL", "13s")
	t.Setenv("EDGE_SSE_MAXIMUM_DURATION", "14m")
	t.Setenv("EDGE_SSE_WRITE_TIMEOUT", "9s")
	t.Setenv("EDGE_PENDING_WATCH_RECONNECT_INITIAL_BACKOFF", "2s")
	t.Setenv("EDGE_PENDING_WATCH_RECONNECT_MAX_BACKOFF", "31s")
	t.Setenv("EDGE_BOOK_STATUS_RECONNECT_INITIAL_BACKOFF", "3s")
	t.Setenv("EDGE_BOOK_STATUS_RECONNECT_MAX_BACKOFF", "32s")
	t.Setenv("EDGE_BOOK_STATUS_DIAL_TIMEOUT", "6s")
	t.Setenv("EDGE_BOOK_STATUS_HEARTBEAT_TIMEOUT", "11s")
	t.Setenv("EDGE_BOOK_STATUS_PREFETCH", "25")
	t.Setenv("EDGE_BOOK_STATUS_QUEUE_MAX_LENGTH_BYTES", "1024")
	t.Setenv("EDGE_REFRESH_COOKIE_MAX_AGE", "168h")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, 7*24*time.Hour, cfg.RefreshCookieMaxAge)
	assert.Equal(t, 12, cfg.QueryRateLimit)
	assert.Equal(t, 30*time.Second, cfg.QueryRateWindow)
	assert.Equal(t, 500, cfg.QueryRateMaxKeys)
	assert.Equal(t, 3, cfg.QueryConcurrency)
	assert.Equal(t, 21, cfg.AuthRegisterRateLimit)
	assert.Equal(t, 2*time.Hour, cfg.AuthRegisterRateWindow)
	assert.Equal(t, 111, cfg.AuthRegisterRateMaxKeys)
	assert.Equal(t, 31, cfg.AuthVerifyEmailRateLimit)
	assert.Equal(t, 70*time.Minute, cfg.AuthVerifyEmailRateWindow)
	assert.Equal(t, 222, cfg.AuthVerifyEmailRateMaxKeys)
	assert.Equal(t, 32, cfg.AuthLoginRateLimit)
	assert.Equal(t, 70*time.Second, cfg.AuthLoginRateWindow)
	assert.Equal(t, 333, cfg.AuthLoginRateMaxKeys)
	assert.Equal(t, 6, cfg.AuthResendVerificationRateLimit)
	assert.Equal(t, 61*time.Minute, cfg.AuthResendVerificationRateWindow)
	assert.Equal(t, 444, cfg.AuthResendVerificationRateMaxKeys)
	assert.Equal(t, 7, cfg.AuthPasswordResetRequestRateLimit)
	assert.Equal(t, 62*time.Minute, cfg.AuthPasswordResetRequestRateWindow)
	assert.Equal(t, 555, cfg.AuthPasswordResetRequestRateMaxKeys)
	assert.Equal(t, 8, cfg.AuthPasswordResetVerifyRateLimit)
	assert.Equal(t, 63*time.Minute, cfg.AuthPasswordResetVerifyRateWindow)
	assert.Equal(t, 666, cfg.AuthPasswordResetVerifyRateMaxKeys)
	assert.Equal(t, 9, cfg.AuthPasswordResetCompleteRateLimit)
	assert.Equal(t, 64*time.Minute, cfg.AuthPasswordResetCompleteRateWindow)
	assert.Equal(t, 777, cfg.AuthPasswordResetCompleteRateMaxKeys)
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
	assert.Equal(t, 11*time.Second, cfg.HTTPReadTimeout)
	assert.Equal(t, 6*time.Second, cfg.HTTPReadHeaderTimeout)
	assert.Equal(t, 7*time.Second, cfg.HTTPWriteTimeoutHeadroom)
	assert.Equal(t, 31*time.Second, cfg.HTTPMinimumWriteTimeout)
	assert.Equal(t, 61*time.Second, cfg.HTTPIdleTimeout)
	assert.Equal(t, 16*time.Second, cfg.HTTPShutdownTimeout)
	assert.Equal(t, 2048, cfg.HTTPMaxHeaderBytes)
	assert.Equal(t, 4*time.Second, cfg.IdentityRPCDeadline)
	assert.Equal(t, 2500*time.Millisecond, cfg.CatalogReadinessTimeout)
	assert.Equal(t, 75*time.Second, cfg.CatalogUploadTimeout)
	assert.Equal(t, 4*time.Second, cfg.CatalogListTimeout)
	assert.Equal(t, 2500*time.Millisecond, cfg.RetrievalReadinessTimeout)
	assert.Equal(t, 7*time.Second, cfg.BooksListTimeout)
	assert.Equal(t, 8*time.Second, cfg.BooksLifecycleTimeout)
	assert.Equal(t, 12*time.Second, cfg.SSEHeartbeatInterval)
	assert.Equal(t, 13*time.Second, cfg.SSERevalidateInterval)
	assert.Equal(t, 14*time.Minute, cfg.SSEMaximumDuration)
	assert.Equal(t, 9*time.Second, cfg.SSEWriteTimeout)
	assert.Equal(t, 2*time.Second, cfg.PendingWatchReconnectInitialBackoff)
	assert.Equal(t, 31*time.Second, cfg.PendingWatchReconnectMaxBackoff)
	assert.Equal(t, 3*time.Second, cfg.BookStatusReconnectInitialBackoff)
	assert.Equal(t, 32*time.Second, cfg.BookStatusReconnectMaxBackoff)
	assert.Equal(t, 6*time.Second, cfg.BookStatusDialTimeout)
	assert.Equal(t, 11*time.Second, cfg.BookStatusHeartbeatTimeout)
	assert.Equal(t, 25, cfg.BookStatusPrefetch)
	assert.Equal(t, 1024, cfg.BookStatusQueueMaxLengthBytes)
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
