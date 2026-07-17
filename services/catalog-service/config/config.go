// Package config loads and validates Catalog runtime configuration.
package config

import (
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
)

// Config is validated Catalog runtime configuration.
type Config struct {
	Address           string
	DSN               string
	MinIOEndpoint     string
	MinIOAccessKey    string
	MinIOSecretKey    string
	MinIOBucket       string
	RabbitURI         string
	MaxUploadBytes    int64
	UploadConcurrency int
	MetricsAddress    string
	TLS               internaltls.Files
	RunAs             process.Identity
}

// Load reads Catalog configuration from the environment.
func Load() (Config, error) {
	dsn, err := readSecret("CATALOG_POSTGRES_DSN_FILE", 4096)
	if err != nil {
		return Config{}, err
	}
	minioAccessKey, err := readSecret("CATALOG_MINIO_ACCESS_KEY_FILE", 1024)
	if err != nil {
		return Config{}, err
	}
	minioSecretKey, err := readSecret("CATALOG_MINIO_SECRET_KEY_FILE", 1024)
	if err != nil {
		return Config{}, err
	}
	rabbitURI, err := readSecret("CATALOG_RABBITMQ_URI_FILE", 4096)
	if err != nil {
		return Config{}, err
	}
	ca, err := required("INTERNAL_TLS_CA_FILE")
	if err != nil {
		return Config{}, err
	}
	cert, err := required("CATALOG_TLS_CERT_FILE")
	if err != nil {
		return Config{}, err
	}
	key, err := required("CATALOG_TLS_KEY_FILE")
	if err != nil {
		return Config{}, err
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
	endpoint, err := required("CATALOG_MINIO_ENDPOINT")
	if err != nil {
		return Config{}, err
	}
	bucket, err := required("CATALOG_MINIO_BUCKET")
	if err != nil {
		return Config{}, err
	}
	maxUploadBytes, err := boundedInt64("CATALOG_MAX_UPLOAD_BYTES", 50<<20, 512<<20)
	if err != nil {
		return Config{}, err
	}
	uploadConcurrency, err := boundedInt("CATALOG_UPLOAD_CONCURRENCY", 2, 16)
	if err != nil {
		return Config{}, err
	}
	metricsAddress, err := privateMetricsAddress(optional("CATALOG_METRICS_ADDR", ":9092"))
	if err != nil {
		return Config{}, err
	}
	return Config{Address: optional("CATALOG_GRPC_ADDR", ":50052"), DSN: dsn, MinIOEndpoint: endpoint, MinIOAccessKey: minioAccessKey, MinIOSecretKey: minioSecretKey, MinIOBucket: bucket, RabbitURI: rabbitURI, MaxUploadBytes: maxUploadBytes, UploadConcurrency: uploadConcurrency, MetricsAddress: metricsAddress, TLS: internaltls.Files{CA: ca, Certificate: cert, Key: key}, RunAs: process.Identity{UID: uid, GID: gid}}, nil
}

func boundedInt64(key string, fallback, maximum int64) (int64, error) {
	value, err := strconv.ParseInt(optional(key, strconv.FormatInt(fallback, 10)), 10, 64)
	if err != nil || value < 1 || value > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, maximum)
	}
	return value, nil
}

func boundedInt(key string, fallback, maximum int) (int, error) {
	value, err := strconv.Atoi(optional(key, strconv.Itoa(fallback)))
	if err != nil || value < 1 || value > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, maximum)
	}
	return value, nil
}

func privateMetricsAddress(value string) (string, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return "", fmt.Errorf("CATALOG_METRICS_ADDR is invalid")
	}
	if host == "" || host == "localhost" {
		return value, nil
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate()) {
		return "", fmt.Errorf("CATALOG_METRICS_ADDR must use a private address")
	}
	return value, nil
}

func readSecret(key string, maxSize int) (string, error) {
	path, err := required(key)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path) // #nosec G304 -- operator-provided secret-file setting.
	if err != nil {
		return "", fmt.Errorf("%s is invalid", key)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > int64(maxSize) {
		return "", fmt.Errorf("%s is invalid", key)
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(maxSize)+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || len(contents) > maxSize || value == "" {
		return "", fmt.Errorf("%s is invalid", key)
	}
	return value, nil
}

func required(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}
func optional(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
