// Package config loads Retrieval runtime configuration without reading secret values.
package config

import (
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
)

type Config struct {
	GRPCAddress      string
	MetricsAddress   string
	TEIURL           string
	QdrantURL        string
	QdrantCollection string
	QdrantAPIKeyFile string
	PostgresDSNFile  string
	TLS              internaltls.Files
	RunAs            process.Identity
}

type WorkerConfig struct {
	DSN, ConsumerRabbitURI, PublisherRabbitURI                    string
	MinIOEndpoint, MinIOAccessKey, MinIOSecretKey, ArtifactBucket string
	MinIOInsecure                                                 bool
	TEIURL, QdrantURL, QdrantCollection, QdrantAPIKey             string
	MetricsAddress                                                string
	Concurrency                                                   int
	RunAs                                                         process.Identity
}

func Load() (Config, error) {
	grpcAddress := os.Getenv("RETRIEVAL_GRPC_ADDR")
	if grpcAddress == "" {
		grpcAddress = os.Getenv("RETRIEVAL_GRPC_ADDRESS")
	}
	collection := os.Getenv("RETRIEVAL_QDRANT_COLLECTION")
	if collection == "" {
		collection = "evidence_v2"
	}
	uid, uidErr := positiveInteger(os.Getenv("RUN_AS_UID"), 65532)
	gid, gidErr := positiveInteger(os.Getenv("RUN_AS_GID"), 65532)
	configuration := Config{
		GRPCAddress: grpcAddress, MetricsAddress: os.Getenv("RETRIEVAL_METRICS_ADDR"), TEIURL: os.Getenv("RETRIEVAL_TEI_URL"),
		QdrantURL: os.Getenv("RETRIEVAL_QDRANT_URL"), QdrantCollection: collection, QdrantAPIKeyFile: os.Getenv("RETRIEVAL_QDRANT_API_KEY_FILE"),
		PostgresDSNFile: os.Getenv("RETRIEVAL_POSTGRES_DSN_FILE"),
		TLS:             internaltls.Files{CA: os.Getenv("RETRIEVAL_TLS_CA_FILE"), Certificate: os.Getenv("RETRIEVAL_TLS_CERT_FILE"), Key: os.Getenv("RETRIEVAL_TLS_KEY_FILE")},
		RunAs:           process.Identity{UID: uid, GID: gid},
	}
	if configuration.GRPCAddress == "" || configuration.QdrantCollection == "" || strings.ContainsAny(configuration.QdrantCollection, "/?#") ||
		configuration.PostgresDSNFile == "" || configuration.QdrantAPIKeyFile == "" || configuration.TLS.CA == "" || configuration.TLS.Certificate == "" || configuration.TLS.Key == "" ||
		!privateServiceURL(configuration.TEIURL) || !privateServiceURL(configuration.QdrantURL) || uidErr != nil || gidErr != nil {
		return Config{}, errors.New("invalid retrieval configuration")
	}
	return configuration, nil
}

func positiveInteger(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, errors.New("invalid process identity")
	}
	return parsed, nil
}

func LoadWorker() (WorkerConfig, error) {
	if os.Getenv("RETRIEVAL_PROCESSING_MODE") != "worker" || os.Getenv("RETRIEVAL_INDEX_PROFILE") != "m5-jina-code-v1" {
		return WorkerConfig{}, errors.New("invalid retrieval processing mode")
	}
	dsn, err := readSecretFile("RETRIEVAL_POSTGRES_DSN_FILE", 4096)
	if err != nil {
		return WorkerConfig{}, err
	}
	consumerURI, err := readSecretFile("RETRIEVAL_RABBITMQ_CONSUMER_URI_FILE", 4096)
	if err != nil {
		return WorkerConfig{}, err
	}
	publisherURI, err := readSecretFile("RETRIEVAL_RABBITMQ_PUBLISHER_URI_FILE", 4096)
	if err != nil {
		return WorkerConfig{}, err
	}
	accessKey, err := readSecretFile("RETRIEVAL_MINIO_ACCESS_KEY_FILE", 1024)
	if err != nil {
		return WorkerConfig{}, err
	}
	secretKey, err := readSecretFile("RETRIEVAL_MINIO_SECRET_KEY_FILE", 1024)
	if err != nil {
		return WorkerConfig{}, err
	}
	qdrantAPIKey, err := readSecretFile("RETRIEVAL_QDRANT_API_KEY_FILE", 1024)
	if err != nil {
		return WorkerConfig{}, err
	}
	uid, uidErr := positiveInteger(os.Getenv("RUN_AS_UID"), 65532)
	gid, gidErr := positiveInteger(os.Getenv("RUN_AS_GID"), 65532)
	concurrency, concurrencyErr := positiveInteger(os.Getenv("RETRIEVAL_WORK_CONCURRENCY"), 1)
	minioInsecure, insecureErr := strconv.ParseBool(os.Getenv("RETRIEVAL_MINIO_INSECURE"))
	configuration := WorkerConfig{DSN: dsn, ConsumerRabbitURI: consumerURI, PublisherRabbitURI: publisherURI,
		MinIOEndpoint: os.Getenv("RETRIEVAL_MINIO_ENDPOINT"), MinIOAccessKey: accessKey, MinIOSecretKey: secretKey, ArtifactBucket: os.Getenv("RETRIEVAL_ARTIFACT_BUCKET"), MinIOInsecure: minioInsecure,
		TEIURL: os.Getenv("RETRIEVAL_TEI_URL"), QdrantURL: os.Getenv("RETRIEVAL_QDRANT_URL"), QdrantCollection: "evidence_v2", QdrantAPIKey: qdrantAPIKey,
		MetricsAddress: os.Getenv("RETRIEVAL_METRICS_ADDR"), Concurrency: concurrency, RunAs: process.Identity{UID: uid, GID: gid}}
	if uidErr != nil || gidErr != nil || concurrencyErr != nil || concurrency > 16 || insecureErr != nil || configuration.MinIOEndpoint == "" ||
		configuration.ArtifactBucket == "" || configuration.MetricsAddress == "" || !privateServiceURL(configuration.TEIURL) || !privateServiceURL(configuration.QdrantURL) {
		return WorkerConfig{}, errors.New("invalid retrieval worker configuration")
	}
	return configuration, nil
}

func readSecretFile(key string, maximum int64) (string, error) {
	path := os.Getenv(key)
	if path == "" {
		return "", errors.New("missing secret file")
	}
	file, err := os.Open(path) // #nosec G304,G703 -- operator-controlled secret path, never derived from public input.
	if err != nil {
		return "", errors.New("invalid secret file")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximum || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("invalid secret file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid secret file")
	}
	return value, nil
}

func privateServiceURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host := parsed.Hostname()
	address := net.ParseIP(host)
	if host == "localhost" || (address != nil && (address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast())) {
		return true
	}
	return !strings.Contains(host, ".")
}
