package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/layout"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/layoutworker"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/memorybudget"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/storage"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/transport"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("layout worker stopped", "reason", "runtime_failure", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.LoadLayout()
	if err != nil {
		return err
	}
	if err = memorybudget.Validate(cfg.WorkConcurrency, cfg.ParserSandboxMemoryBytes, cfg.ParserRuntimeHeadroomBytes); err != nil {
		return err
	}
	if err = process.DropPrivileges(cfg.RunAs); err != nil {
		return err
	}
	if err = extractor.VerifySandbox(ctx); err != nil {
		return err
	}
	minioClient, err := newMinIOClient(cfg)
	if err != nil {
		return err
	}
	sources := storage.NewSourceStore(minioClient, cfg.SourceBucket)
	analyzer, err := layout.NewPopplerAnalyzer(layout.AnalyzerConfig{
		PDFTextPath: cfg.PDFTextPath, EPUBParserPath: cfg.EPUBParserPath,
		MaximumPages: cfg.MaximumPages, MaximumItemsPerLocation: cfg.MaximumItemsPerLocation,
		MaximumXMLTokens: cfg.MaximumXMLTokens, MaximumXMLDepth: cfg.MaximumXMLDepth,
		MaximumOutputBytes: cfg.MaximumExtractedBytes, MaximumPageTextBytes: cfg.MaximumPageBytes,
		MaximumItemTextBytes: cfg.MaximumItemTextBytes, MaximumTextBytes: cfg.MaximumExtractedBytes,
		EPUBArchiveLimits: extractor.EPUBArchiveLimits{
			MaximumEntries: cfg.EPUBMaximumEntries, MaximumSpineItems: cfg.EPUBMaximumSpineItems,
			MaximumEntryBytes: cfg.EPUBMaximumEntryBytes, MaximumExpandedBytes: cfg.EPUBMaximumExpandedBytes,
			MaximumTextBytes: cfg.EPUBMaximumTextBytes,
		},
	}, nil)
	if err != nil {
		return err
	}
	service, err := layoutworker.NewService(sources, analyzer, layoutworker.Config{
		MaximumSourceBytes: cfg.MaximumSourceBytes, MaximumRanges: cfg.MaximumRanges,
		MaximumExcludedRatio: cfg.MaximumExcludedRatio, MinimumSignals: cfg.MinimumSignals, PolicyVersion: cfg.PolicyVersion,
		ParserVersion: cfg.ParserVersion, ModelSHA256: cfg.ModelSHA256, ParserTimeout: cfg.ParserTimeout,
	})
	if err != nil {
		return err
	}
	brokerPolicy := transport.BrokerPolicy{DialTimeout: cfg.RabbitDialTimeout, Heartbeat: cfg.RabbitHeartbeat}
	connection, err := transport.DialConsumer(ctx, cfg.RabbitURI, brokerPolicy)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	channel, err := connection.Channel()
	if err != nil {
		return errors.New("layout worker broker channel unavailable")
	}
	defer func() { _ = channel.Close() }()
	publisher := transport.NewReconnectingPublisher(cfg.RabbitURI, brokerPolicy)
	defer func() { _ = publisher.Close() }()
	consumer, err := layoutworker.NewConsumer(channel, cfg.Queue, cfg.ResultExchange, cfg.WorkConcurrency, service, publisher, cfg.RabbitPublishTimeout)
	if err != nil {
		return err
	}
	logger.Info("layout worker started", "policy_version", cfg.PolicyVersion, "parser_version", cfg.ParserVersion)
	return consumer.Run(ctx, cfg.WorkConcurrency)
}

func newMinIOClient(cfg config.LayoutConfig) (*minio.Client, error) {
	transportValue := http.DefaultTransport.(*http.Transport).Clone()
	transportValue.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.MinIOCAFile != "" {
		contents, err := os.ReadFile(cfg.MinIOCAFile) // #nosec G304 -- trusted operator path.
		if err != nil {
			return nil, errors.New("object storage CA unavailable")
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(contents) {
			return nil, errors.New("object storage CA invalid")
		}
		transportValue.TLSClientConfig.RootCAs = roots
	}
	client, err := minio.New(cfg.MinIOEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, ""),
		Secure: !cfg.MinIOInsecure, Transport: transportValue,
	})
	if err != nil {
		return nil, errors.New("object storage configuration invalid")
	}
	return client, nil
}
