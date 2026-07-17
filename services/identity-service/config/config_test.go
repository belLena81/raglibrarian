package config_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

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
}

func TestLoadRejectsInvalidBcryptConfiguration(t *testing.T) {
	setRequired(t)
	t.Setenv("IDENTITY_BCRYPT_CONCURRENCY", "0")
	_, err := config.Load()
	assert.Error(t, err)
}
