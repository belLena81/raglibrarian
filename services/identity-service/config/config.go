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
	"time"

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
	Address                    string
	DSN                        string
	SigningKey                 []byte
	SigningKeyID               string
	FingerprintKey             []byte
	OutboxKey                  []byte
	PasswordResetKey           []byte
	OutboxKeyID                string
	BootstrapVerifier          []byte
	BcryptConcurrency          int
	VerificationTTL            time.Duration
	VerificationRetention      time.Duration
	VerificationResendCooldown time.Duration
	PasswordResetCodeTTL       time.Duration
	PasswordResetGrantTTL      time.Duration
	RejectedRetention          time.Duration
	AccessTokenTTL             time.Duration
	SessionTTL                 time.Duration
	DBPingTimeout              time.Duration
	HealthProbeTimeout         time.Duration
	HealthPollInterval         time.Duration
	EmailDeliverInterval       time.Duration
	EmailClaimTTL              time.Duration
	EmailClaimBatchSize        int
	EmailRetryBaseInterval     time.Duration
	EmailRetryMaxInterval      time.Duration
	EmailRetryMaxAttempts      int
	SessionCleanupTimeout      time.Duration
	SessionCleanupInterval     time.Duration
	IdentityCleanupTimeout     time.Duration
	IdentityCleanupInterval    time.Duration
	GRPCGracefulStopTimeout    time.Duration
	GRPCOperationTimeout       time.Duration
	TLS                        internaltls.Files
	RunAs                      process.Identity
	SMTP                       email.Config
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
	smtpOperationTimeout, err := durationOptional("IDENTITY_SMTP_OPERATION_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	verificationTTL, err := durationOptional("IDENTITY_VERIFICATION_TTL", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	verificationRetention, err := durationOptional("IDENTITY_VERIFICATION_RETENTION", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	verificationResendCooldown, err := durationOptional("IDENTITY_VERIFICATION_RESEND_COOLDOWN", 10*time.Minute)
	if err != nil {
		return Config{}, err
	}
	passwordResetCodeTTL, err := durationOptional("IDENTITY_PASSWORD_RESET_CODE_TTL", 10*time.Minute)
	if err != nil {
		return Config{}, err
	}
	passwordResetGrantTTL, err := durationOptional("IDENTITY_PASSWORD_RESET_GRANT_TTL", 10*time.Minute)
	if err != nil {
		return Config{}, err
	}
	rejectedRetention, err := durationOptional("IDENTITY_REJECTED_RETENTION", 90*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	accessTokenTTL, err := durationOptional("IDENTITY_ACCESS_TOKEN_TTL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	sessionTTL, err := durationOptional("IDENTITY_SESSION_TTL", 30*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	dbPingTimeout, err := durationOptional("IDENTITY_DB_PING_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	healthProbeTimeout, err := durationOptional("IDENTITY_HEALTH_PROBE_TIMEOUT", 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	healthPollInterval, err := durationOptional("IDENTITY_HEALTH_POLL_INTERVAL", 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	emailDeliverInterval, err := durationOptional("IDENTITY_EMAIL_DELIVER_INTERVAL", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	emailClaimTTL, err := durationOptional("IDENTITY_EMAIL_CLAIM_TTL", time.Minute)
	if err != nil {
		return Config{}, err
	}
	emailRetryBaseInterval, err := durationOptional("IDENTITY_EMAIL_RETRY_BASE_INTERVAL", time.Minute)
	if err != nil {
		return Config{}, err
	}
	emailRetryMaxInterval, err := durationOptional("IDENTITY_EMAIL_RETRY_MAX_INTERVAL", 60*time.Minute)
	if err != nil {
		return Config{}, err
	}
	emailClaimBatchSize, err := strconv.Atoi(optional("IDENTITY_EMAIL_CLAIM_BATCH_SIZE", "25"))
	if err != nil || emailClaimBatchSize < 1 {
		return Config{}, fmt.Errorf("IDENTITY_EMAIL_CLAIM_BATCH_SIZE must be positive")
	}
	emailRetryMaxAttempts, err := strconv.Atoi(optional("IDENTITY_EMAIL_RETRY_MAX_ATTEMPTS", "10"))
	if err != nil || emailRetryMaxAttempts < 1 {
		return Config{}, fmt.Errorf("IDENTITY_EMAIL_RETRY_MAX_ATTEMPTS must be positive")
	}
	sessionCleanupTimeout, err := durationOptional("IDENTITY_SESSION_CLEANUP_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	sessionCleanupInterval, err := durationOptional("IDENTITY_SESSION_CLEANUP_INTERVAL", time.Hour)
	if err != nil {
		return Config{}, err
	}
	identityCleanupTimeout, err := durationOptional("IDENTITY_STATE_CLEANUP_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	identityCleanupInterval, err := durationOptional("IDENTITY_STATE_CLEANUP_INTERVAL", time.Hour)
	if err != nil {
		return Config{}, err
	}
	grpcGracefulStopTimeout, err := durationOptional("IDENTITY_GRPC_GRACEFUL_STOP_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	grpcOperationTimeout, err := durationOptional("IDENTITY_GRPC_OPERATION_TIMEOUT", 5*time.Second)
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
		VerificationTTL: verificationTTL, VerificationRetention: verificationRetention,
		VerificationResendCooldown: verificationResendCooldown, PasswordResetCodeTTL: passwordResetCodeTTL,
		PasswordResetGrantTTL: passwordResetGrantTTL, RejectedRetention: rejectedRetention,
		AccessTokenTTL: accessTokenTTL, SessionTTL: sessionTTL,
		DBPingTimeout: dbPingTimeout, HealthProbeTimeout: healthProbeTimeout, HealthPollInterval: healthPollInterval,
		EmailDeliverInterval: emailDeliverInterval, EmailClaimTTL: emailClaimTTL, EmailClaimBatchSize: emailClaimBatchSize,
		EmailRetryBaseInterval: emailRetryBaseInterval, EmailRetryMaxInterval: emailRetryMaxInterval, EmailRetryMaxAttempts: emailRetryMaxAttempts,
		SessionCleanupTimeout: sessionCleanupTimeout, SessionCleanupInterval: sessionCleanupInterval,
		IdentityCleanupTimeout: identityCleanupTimeout, IdentityCleanupInterval: identityCleanupInterval, GRPCGracefulStopTimeout: grpcGracefulStopTimeout,
		GRPCOperationTimeout: grpcOperationTimeout,
		TLS:                  internaltls.Files{CA: ca, Certificate: cert, Key: keyFile},
		RunAs:                process.Identity{UID: uid, GID: gid},
		SMTP: email.Config{
			Address:    optional("IDENTITY_SMTP_ADDR", "mailpit:1025"),
			ServerName: optional("IDENTITY_SMTP_SERVER_NAME", "mailpit"),
			Username:   os.Getenv("IDENTITY_SMTP_USERNAME"), Password: smtpPassword,
			From:             optional("IDENTITY_SMTP_FROM", "noreply@raglibrarian.local"),
			VerifyURL:        optional("IDENTITY_VERIFY_URL", "http://localhost:5173/verify-email"),
			StartTLS:         optional("IDENTITY_SMTP_STARTTLS", "false") == "true",
			OperationTimeout: smtpOperationTimeout,
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

func durationOptional(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return parsed, nil
}
