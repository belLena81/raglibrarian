// Package config loads and validates Identity runtime configuration.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/identity-service/email"
)

// ErrSecretFile reports a missing, unreadable, over-sized, or insecurely
// permissioned secret file without exposing its path or contents.
var ErrSecretFile = errors.New("secret file is invalid")

// Config contains validated runtime settings and file-loaded secrets for the
// Identity service.
type Config struct {
	Address           string
	DSN               string
	SigningKey        []byte
	SigningKeyID      string
	FingerprintKey    []byte
	OutboxKey         []byte
	PasswordResetKey  []byte
	OutboxKeyID       string
	BootstrapVerifier []byte
	BcryptConcurrency int
	TLS               internaltls.Files
	RunAs             process.Identity
	SMTP              email.Config
}

// Load reads Identity configuration from the process environment and secret
// files, rejecting missing or unsafe values.
func Load() (Config, error) {
	dsn, err := readSecret("IDENTITY_POSTGRES_DSN_FILE", 4096)
	if err != nil {
		return Config{}, err
	}
	signingKey, err := readHexSecret("IDENTITY_SIGNING_KEY_FILE", 64)
	if err != nil {
		return Config{}, err
	}
	fingerprintKey, err := readHexSecret("IDENTITY_EMAIL_FINGERPRINT_KEY_FILE", 32)
	if err != nil {
		return Config{}, err
	}
	outboxKey, err := readHexSecret("IDENTITY_EMAIL_OUTBOX_KEY_FILE", 32)
	if err != nil {
		return Config{}, err
	}
	passwordResetKey, err := readHexSecret("IDENTITY_PASSWORD_RESET_HMAC_KEY_FILE", 32)
	if err != nil {
		return Config{}, err
	}
	bootstrapVerifier, err := readOptionalSecret("IDENTITY_BOOTSTRAP_VERIFIER_FILE", 32)
	if err != nil {
		return Config{}, err
	}
	smtpPassword, err := readSecret("IDENTITY_SMTP_PASSWORD_FILE", 1024)
	if err != nil {
		return Config{}, err
	}
	concurrency, err := strconv.Atoi(optional("IDENTITY_BCRYPT_CONCURRENCY", "4"))
	if err != nil || concurrency < 1 || concurrency > 64 {
		return Config{}, fmt.Errorf("IDENTITY_BCRYPT_CONCURRENCY must be between 1 and 64")
	}
	uid, err := strconv.Atoi(optional("RUN_AS_UID", "65532"))
	if err != nil {
		return Config{}, fmt.Errorf("RUN_AS_UID: %w", err)
	}
	gid, err := strconv.Atoi(optional("RUN_AS_GID", "65532"))
	if err != nil {
		return Config{}, fmt.Errorf("RUN_AS_GID: %w", err)
	}
	if uid < 1 || gid < 1 {
		return Config{}, fmt.Errorf("RUN_AS_UID and RUN_AS_GID must be positive")
	}
	ca, err := required("INTERNAL_TLS_CA_FILE")
	if err != nil {
		return Config{}, err
	}
	cert, err := required("IDENTITY_TLS_CERT_FILE")
	if err != nil {
		return Config{}, err
	}
	keyFile, err := required("IDENTITY_TLS_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	return Config{
		Address: optional("IDENTITY_GRPC_ADDR", ":50051"), DSN: dsn,
		SigningKey: signingKey, FingerprintKey: fingerprintKey, OutboxKey: outboxKey, PasswordResetKey: passwordResetKey,
		SigningKeyID:      optional("IDENTITY_SIGNING_KEY_ID", "local-v1"),
		OutboxKeyID:       optional("IDENTITY_EMAIL_OUTBOX_KEY_ID", "local-v1"),
		BootstrapVerifier: bootstrapVerifier, BcryptConcurrency: concurrency,
		TLS:   internaltls.Files{CA: ca, Certificate: cert, Key: keyFile},
		RunAs: process.Identity{UID: uid, GID: gid},
		SMTP: email.Config{
			Address:    optional("IDENTITY_SMTP_ADDR", "mailpit:1025"),
			ServerName: optional("IDENTITY_SMTP_SERVER_NAME", "mailpit"),
			Username:   os.Getenv("IDENTITY_SMTP_USERNAME"), Password: smtpPassword,
			From:      optional("IDENTITY_SMTP_FROM", "noreply@raglibrarian.local"),
			VerifyURL: optional("IDENTITY_VERIFY_URL", "http://localhost:5173/verify-email"),
			StartTLS:  optional("IDENTITY_SMTP_STARTTLS", "false") == "true",
		},
	}, nil
}

func readHexSecret(key string, size int) ([]byte, error) {
	value, err := readSecret(key, size*2+2)
	if err != nil {
		return nil, err
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size {
		return nil, fmt.Errorf("%w: %s", ErrSecretFile, key)
	}
	return decoded, nil
}

func readOptionalSecret(key string, exactSize int) ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(key))
	if path == "" {
		return nil, nil
	}
	value, err := readSecretBytes(path, exactSize)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSecretFile, key)
	}
	if len(value) == exactSize {
		return value, nil
	}
	decoded, decodeErr := hex.DecodeString(strings.TrimSpace(string(value)))
	if decodeErr != nil || len(decoded) != exactSize {
		return nil, fmt.Errorf("%w: %s", ErrSecretFile, key)
	}
	return decoded, nil
}

func readSecret(key string, maxSize int) (string, error) {
	path, err := required(key)
	if err != nil {
		return "", err
	}
	contents, err := readSecretBytes(path, maxSize)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrSecretFile, key)
	}
	value := strings.TrimSpace(string(contents))
	if value == "" {
		return "", fmt.Errorf("%w: %s", ErrSecretFile, key)
	}
	return value, nil
}

func readSecretBytes(path string, maxSize int) ([]byte, error) {
	// The path comes from an operator-controlled *_FILE setting. Opening it is
	// intentional; validation below bounds its type, permissions, and size.
	file, err := os.Open(path) // #nosec G304,G703 -- dedicated secret-file path configured by the operator.
	if err != nil {
		return nil, ErrSecretFile
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(maxSize) || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrSecretFile
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(maxSize)+1))
	if err != nil || len(contents) > maxSize {
		return nil, ErrSecretFile
	}
	return contents, nil
}

func required(key string) (string, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func optional(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
