package config_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/identity-service/config"
)

func setRequired(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	secret := func(name, value string) string {
		path := filepath.Join(directory, name)
		require.NoError(t, os.WriteFile(path, []byte(value), 0o600))
		return path
	}
	t.Setenv("IDENTITY_POSTGRES_DSN_FILE", secret("dsn", "postgres://fixture"))
	t.Setenv("IDENTITY_SIGNING_KEY_FILE", secret("signing", hex.EncodeToString(make([]byte, 64))))
	t.Setenv("IDENTITY_EMAIL_FINGERPRINT_KEY_FILE", secret("fingerprint", hex.EncodeToString(make([]byte, 32))))
	t.Setenv("IDENTITY_EMAIL_OUTBOX_KEY_FILE", secret("outbox", hex.EncodeToString(make([]byte, 32))))
	t.Setenv("IDENTITY_PASSWORD_RESET_HMAC_KEY_FILE", secret("password-reset", hex.EncodeToString(make([]byte, 32))))
	t.Setenv("IDENTITY_SMTP_PASSWORD_FILE", secret("smtp", "fixture-password"))
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
	assert.Equal(t, 10*time.Second, cfg.SMTP.OperationTimeout)
	assert.Equal(t, 30*time.Minute, cfg.VerificationTTL)
	assert.Equal(t, 24*time.Hour, cfg.VerificationRetention)
	assert.Equal(t, 10*time.Minute, cfg.VerificationResendCooldown)
	assert.Equal(t, 10*time.Minute, cfg.PasswordResetCodeTTL)
	assert.Equal(t, 10*time.Minute, cfg.PasswordResetGrantTTL)
	assert.Equal(t, 90*24*time.Hour, cfg.RejectedRetention)
	assert.Equal(t, 15*time.Minute, cfg.AccessTokenTTL)
	assert.Equal(t, 30*24*time.Hour, cfg.SessionTTL)
	assert.Equal(t, 5*time.Second, cfg.DBPingTimeout)
	assert.Equal(t, 2*time.Second, cfg.HealthProbeTimeout)
	assert.Equal(t, 2*time.Second, cfg.HealthPollInterval)
	assert.Equal(t, 5*time.Second, cfg.EmailDeliverInterval)
	assert.Equal(t, time.Minute, cfg.EmailClaimTTL)
	assert.Equal(t, 25, cfg.EmailClaimBatchSize)
	assert.Equal(t, time.Minute, cfg.EmailRetryBaseInterval)
	assert.Equal(t, 60*time.Minute, cfg.EmailRetryMaxInterval)
	assert.Equal(t, 10, cfg.EmailRetryMaxAttempts)
	assert.Equal(t, 5*time.Second, cfg.SessionCleanupTimeout)
	assert.Equal(t, time.Hour, cfg.SessionCleanupInterval)
	assert.Equal(t, 30*time.Second, cfg.IdentityCleanupTimeout)
	assert.Equal(t, time.Hour, cfg.IdentityCleanupInterval)
	assert.Equal(t, 10*time.Second, cfg.GRPCGracefulStopTimeout)
	assert.Equal(t, 5*time.Second, cfg.GRPCOperationTimeout)
}

func TestLoadRejectsInvalidBcryptConfiguration(t *testing.T) {
	setRequired(t)
	t.Setenv("IDENTITY_BCRYPT_CONCURRENCY", "0")
	_, err := config.Load()
	assert.Error(t, err)
}

func TestLoadParsesSMTPTimeoutConfiguration(t *testing.T) {
	setRequired(t)
	t.Setenv("IDENTITY_SMTP_OPERATION_TIMEOUT", "12s")
	t.Setenv("IDENTITY_VERIFICATION_TTL", "31m")
	t.Setenv("IDENTITY_VERIFICATION_RETENTION", "25h")
	t.Setenv("IDENTITY_VERIFICATION_RESEND_COOLDOWN", "11m")
	t.Setenv("IDENTITY_PASSWORD_RESET_CODE_TTL", "12m")
	t.Setenv("IDENTITY_PASSWORD_RESET_GRANT_TTL", "13m")
	t.Setenv("IDENTITY_REJECTED_RETENTION", "2161h")
	t.Setenv("IDENTITY_ACCESS_TOKEN_TTL", "16m")
	t.Setenv("IDENTITY_SESSION_TTL", "721h")
	t.Setenv("IDENTITY_DB_PING_TIMEOUT", "6s")
	t.Setenv("IDENTITY_HEALTH_PROBE_TIMEOUT", "3s")
	t.Setenv("IDENTITY_HEALTH_POLL_INTERVAL", "4s")
	t.Setenv("IDENTITY_EMAIL_DELIVER_INTERVAL", "7s")
	t.Setenv("IDENTITY_EMAIL_CLAIM_TTL", "61s")
	t.Setenv("IDENTITY_EMAIL_CLAIM_BATCH_SIZE", "26")
	t.Setenv("IDENTITY_EMAIL_RETRY_BASE_INTERVAL", "62s")
	t.Setenv("IDENTITY_EMAIL_RETRY_MAX_INTERVAL", "63m")
	t.Setenv("IDENTITY_EMAIL_RETRY_MAX_ATTEMPTS", "11")
	t.Setenv("IDENTITY_SESSION_CLEANUP_TIMEOUT", "8s")
	t.Setenv("IDENTITY_SESSION_CLEANUP_INTERVAL", "2h")
	t.Setenv("IDENTITY_STATE_CLEANUP_TIMEOUT", "31s")
	t.Setenv("IDENTITY_STATE_CLEANUP_INTERVAL", "3h")
	t.Setenv("IDENTITY_GRPC_GRACEFUL_STOP_TIMEOUT", "11s")
	t.Setenv("IDENTITY_GRPC_OPERATION_TIMEOUT", "9s")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, 12*time.Second, cfg.SMTP.OperationTimeout)
	assert.Equal(t, 31*time.Minute, cfg.VerificationTTL)
	assert.Equal(t, 25*time.Hour, cfg.VerificationRetention)
	assert.Equal(t, 11*time.Minute, cfg.VerificationResendCooldown)
	assert.Equal(t, 12*time.Minute, cfg.PasswordResetCodeTTL)
	assert.Equal(t, 13*time.Minute, cfg.PasswordResetGrantTTL)
	assert.Equal(t, 2161*time.Hour, cfg.RejectedRetention)
	assert.Equal(t, 16*time.Minute, cfg.AccessTokenTTL)
	assert.Equal(t, 721*time.Hour, cfg.SessionTTL)
	assert.Equal(t, 6*time.Second, cfg.DBPingTimeout)
	assert.Equal(t, 3*time.Second, cfg.HealthProbeTimeout)
	assert.Equal(t, 4*time.Second, cfg.HealthPollInterval)
	assert.Equal(t, 7*time.Second, cfg.EmailDeliverInterval)
	assert.Equal(t, 61*time.Second, cfg.EmailClaimTTL)
	assert.Equal(t, 26, cfg.EmailClaimBatchSize)
	assert.Equal(t, 62*time.Second, cfg.EmailRetryBaseInterval)
	assert.Equal(t, 63*time.Minute, cfg.EmailRetryMaxInterval)
	assert.Equal(t, 11, cfg.EmailRetryMaxAttempts)
	assert.Equal(t, 8*time.Second, cfg.SessionCleanupTimeout)
	assert.Equal(t, 2*time.Hour, cfg.SessionCleanupInterval)
	assert.Equal(t, 31*time.Second, cfg.IdentityCleanupTimeout)
	assert.Equal(t, 3*time.Hour, cfg.IdentityCleanupInterval)
	assert.Equal(t, 11*time.Second, cfg.GRPCGracefulStopTimeout)
	assert.Equal(t, 9*time.Second, cfg.GRPCOperationTimeout)
}

func TestLoadRejectsInvalidSMTPTimeoutConfiguration(t *testing.T) {
	setRequired(t)
	t.Setenv("IDENTITY_SMTP_OPERATION_TIMEOUT", "0s")

	_, err := config.Load()

	assert.Error(t, err)
}
