// Package config loads and validates Edge runtime configuration.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
)

var (
	// ErrRequiredConfiguration identifies a missing required setting.
	ErrRequiredConfiguration = errors.New("required configuration missing")
	// ErrVerifyKeyConfiguration identifies an invalid access-token verification key.
	ErrVerifyKeyConfiguration = errors.New("verify key configuration invalid")
	// ErrTrustedProxyConfiguration identifies an invalid trusted-proxy CIDR allowlist.
	ErrTrustedProxyConfiguration = errors.New("trusted proxy configuration invalid")
	// ErrRefreshCookieConfiguration identifies an invalid refresh-cookie policy setting.
	ErrRefreshCookieConfiguration = errors.New("refresh cookie configuration invalid")
	// ErrRunIdentityConfiguration identifies an invalid runtime UID or GID.
	ErrRunIdentityConfiguration = errors.New("run identity configuration invalid")
)

// Config is validated Edge runtime configuration.
type Config struct {
	Addr, IdentityAddress, CatalogAddress, RetrievalAddress string
	StatusRabbitURI, StatusQueue                            string
	VerifyKey                                               []byte
	PreviousVerifyKey                                       []byte
	TrustedProxyCIDRs                                       []netip.Prefix
	TLS                                                     internaltls.Files
	SecureCookie                                            bool
	PublicOrigin                                            string
	EnforceBrowserOrigin                                    bool
	RunAs                                                   process.Identity
}

// Load reads Edge configuration from the environment.
func Load() (Config, error) {
	statusRabbitURI, err := readSecret("EDGE_STATUS_RABBITMQ_URI_FILE", 4096)
	if err != nil {
		return Config{}, err
	}
	keyHex, err := required("EDGE_VERIFY_KEY")
	if err != nil {
		return Config{}, err
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return Config{}, fmt.Errorf("%w: EDGE_VERIFY_KEY must be 64 hex characters", ErrVerifyKeyConfiguration)
	}
	var previousKey []byte
	if previousHex := strings.TrimSpace(os.Getenv("EDGE_PREVIOUS_VERIFY_KEY")); previousHex != "" {
		previousKey, err = hex.DecodeString(previousHex)
		if err != nil || len(previousKey) != 32 {
			return Config{}, fmt.Errorf("%w: EDGE_PREVIOUS_VERIFY_KEY must be 64 hex characters", ErrVerifyKeyConfiguration)
		}
	}
	prefixes, err := parseCIDRs(os.Getenv("EDGE_TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return Config{}, err
	}
	insecureCookie, err := strconv.ParseBool(optional("EDGE_INSECURE_REFRESH_COOKIE", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("%w: EDGE_INSECURE_REFRESH_COOKIE: %w", ErrRefreshCookieConfiguration, err)
	}
	enforceOrigin, err := strconv.ParseBool(optional("EDGE_ENFORCE_BROWSER_ORIGIN", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("EDGE_ENFORCE_BROWSER_ORIGIN: %w", err)
	}
	publicOrigin := strings.TrimRight(strings.TrimSpace(os.Getenv("EDGE_PUBLIC_ORIGIN")), "/")
	if enforceOrigin && publicOrigin == "" {
		return Config{}, fmt.Errorf("EDGE_PUBLIC_ORIGIN is required when browser origin enforcement is enabled")
	}
	if enforceOrigin {
		parsedOrigin, parseErr := url.Parse(publicOrigin)
		if parseErr != nil || parsedOrigin.Host == "" || parsedOrigin.Path != "" || parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" {
			return Config{}, fmt.Errorf("EDGE_PUBLIC_ORIGIN must be an absolute origin")
		}
		if parsedOrigin.Scheme != "https" && !insecureCookie {
			return Config{}, fmt.Errorf("EDGE_PUBLIC_ORIGIN must use HTTPS")
		}
	}
	runAs, err := processIdentity()
	if err != nil {
		return Config{}, err
	}
	ca, err := required("INTERNAL_TLS_CA_FILE")
	if err != nil {
		return Config{}, err
	}
	cert, err := required("EDGE_TLS_CERT_FILE")
	if err != nil {
		return Config{}, err
	}
	keyFile, err := required("EDGE_TLS_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	return Config{
		Addr:                 optional("QUERY_ADDR", ":8080"),
		IdentityAddress:      optional("IDENTITY_GRPC_ADDR", "identity-service:50051"),
		CatalogAddress:       optional("CATALOG_GRPC_ADDR", "catalog-service:50052"),
		RetrievalAddress:     optional("RETRIEVAL_GRPC_ADDR", "retrieval-service:50054"),
		StatusRabbitURI:      statusRabbitURI,
		StatusQueue:          optional("EDGE_STATUS_QUEUE", "edge.book-status.local.1"),
		VerifyKey:            key,
		PreviousVerifyKey:    previousKey,
		TrustedProxyCIDRs:    prefixes,
		TLS:                  internaltls.Files{CA: ca, Certificate: cert, Key: keyFile},
		SecureCookie:         !insecureCookie,
		PublicOrigin:         publicOrigin,
		EnforceBrowserOrigin: enforceOrigin,
		RunAs:                runAs,
	}, nil
}

func parseCIDRs(value string) ([]netip.Prefix, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("%w: EDGE_TRUSTED_PROXY_CIDRS: %w", ErrTrustedProxyConfiguration, err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func processIdentity() (process.Identity, error) {
	uid, err := strconv.Atoi(optional("RUN_AS_UID", "65532"))
	if err != nil {
		return process.Identity{}, fmt.Errorf("%w: RUN_AS_UID: %w", ErrRunIdentityConfiguration, err)
	}
	gid, err := strconv.Atoi(optional("RUN_AS_GID", "65532"))
	if err != nil {
		return process.Identity{}, fmt.Errorf("%w: RUN_AS_GID: %w", ErrRunIdentityConfiguration, err)
	}
	if uid < 1 || gid < 1 {
		return process.Identity{}, fmt.Errorf("%w: RUN_AS_UID and RUN_AS_GID must be positive", ErrRunIdentityConfiguration)
	}
	return process.Identity{UID: uid, GID: gid}, nil
}

func required(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%w: %s is required", ErrRequiredConfiguration, key)
	}
	return value, nil
}
func optional(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func readSecret(envKey string, maxBytes int64) (string, error) {
	path, err := required(envKey)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path) // #nosec G304 -- operator-provided secret path.
	if err != nil {
		return "", fmt.Errorf("read %s: %w", envKey, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxBytes {
		return "", fmt.Errorf("%w: %s secret file is invalid", ErrRequiredConfiguration, envKey)
	}
	value, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", envKey, err)
	}
	if len(value) == 0 || int64(len(value)) > maxBytes || strings.TrimSpace(string(value)) == "" {
		return "", fmt.Errorf("%w: %s secret is empty or too large", ErrRequiredConfiguration, envKey)
	}
	return strings.TrimSpace(string(value)), nil
}
