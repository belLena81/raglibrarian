// Package bootstrap constructs the shared Ingestion application and outward adapters.
package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/metrics"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/repository"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/storage"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/transport"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Runtime struct {
	Config       config.Config
	Processor    *application.Processor
	Repository   *repository.Postgres
	Outbox       *transport.OutboxWorker
	Publisher    *transport.ReconnectingPublisher
	Cleaner      *artifact.Cleaner
	Metrics      *metrics.Recorder
	Diagnostics  *diagnostic.Logger
	pool         *pgxpool.Pool
	publisher    *transport.ReconnectingPublisher
	storageProbe func(context.Context) bool
}

type CleanupRuntime struct {
	Cleaner *artifact.Cleaner
	pool    *pgxpool.Pool
}

type DispatcherRuntime struct {
	Outbox    *transport.OutboxWorker
	Publisher *transport.ReconnectingPublisher
	pool      *pgxpool.Pool
}

func NewDispatcher(ctx context.Context, cfg config.Config) (*DispatcherRuntime, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, errors.New("database configuration invalid")
	}
	poolConfig.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.New("database unavailable")
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("database unavailable")
	}
	publisher := transport.NewReconnectingPublisher(cfg.RabbitURI)
	outbox, err := transport.NewOutboxWorker(repository.NewPostgres(pool), publisher, cfg.ResultExchange, cfg.OutboxInterval)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &DispatcherRuntime{Outbox: outbox, Publisher: publisher, pool: pool}, nil
}

func (r *DispatcherRuntime) Close() {
	_ = r.Publisher.Close()
	r.pool.Close()
}

func NewCleanup(ctx context.Context, cfg config.CleanupConfig) (*CleanupRuntime, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, errors.New("database configuration invalid")
	}
	poolConfig.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.New("database unavailable")
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("database unavailable")
	}
	var artifactStore artifact.PrefixStore
	if cfg.RuntimeBackend == "aws" {
		client, clientErr := storage.NewAWSS3Client(ctx, cfg.AWSRegion)
		if clientErr != nil {
			pool.Close()
			return nil, clientErr
		}
		artifactStore, err = storage.NewAWSArtifactStore(client, cfg.ArtifactBucket, cfg.KMSKeyARN)
	} else {
		var minioClient *minio.Client
		minioClient, err = newMinIOClient(minIOConfig{endpoint: cfg.MinIOEndpoint, accessKey: cfg.MinIOAccessKey, secretKey: cfg.MinIOSecretKey, caFile: cfg.MinIOCAFile, insecure: cfg.MinIOInsecure})
		if err == nil {
			artifactStore = storage.NewArtifactStore(minioClient, cfg.ArtifactBucket)
		}
	}
	if err != nil {
		pool.Close()
		return nil, err
	}
	cleaner, err := artifact.NewCleaner(repository.NewPostgres(pool), artifactStore, cfg.CleanupInterval, cfg.OrphanGracePeriod)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &CleanupRuntime{Cleaner: cleaner, pool: pool}, nil
}

func (r *CleanupRuntime) Close() { r.pool.Close() }

func New(ctx context.Context, cfg config.Config) (*Runtime, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, errors.New("database configuration invalid")
	}
	poolConfig.MaxConns = int32(cfg.WorkConcurrency + 3) // #nosec G115 -- validated small bound.
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.New("database unavailable")
	}
	cleanup := func() { pool.Close() }
	if err = pool.Ping(ctx); err != nil {
		cleanup()
		return nil, errors.New("database unavailable")
	}
	var sourceStore application.SourceReader
	var storageProbe func(context.Context) bool
	var artifactStore interface {
		artifact.Store
		artifact.PrefixStore
	}
	if cfg.RuntimeBackend == "aws" {
		client, clientErr := storage.NewAWSS3Client(ctx, cfg.AWSRegion)
		if clientErr != nil {
			cleanup()
			return nil, clientErr
		}
		var awsSource *storage.AWSSourceStore
		awsSource, err = storage.NewAWSSourceStore(client, cfg.SourceBucket)
		if err == nil {
			sourceStore = awsSource
			var awsArtifact *storage.AWSArtifactStore
			awsArtifact, err = storage.NewAWSArtifactStore(client, cfg.ArtifactBucket, cfg.KMSKeyARN)
			if err == nil {
				artifactStore = awsArtifact
				storageProbe = func(probeCtx context.Context) bool { return storage.AllReady(probeCtx, awsSource, awsArtifact) }
			}
		}
	} else {
		var minioClient *minio.Client
		minioClient, err = newMinIOClient(minIOConfig{endpoint: cfg.MinIOEndpoint, accessKey: cfg.MinIOAccessKey, secretKey: cfg.MinIOSecretKey, caFile: cfg.MinIOCAFile, insecure: cfg.MinIOInsecure})
		if err == nil {
			minioSource := storage.NewSourceStore(minioClient, cfg.SourceBucket)
			minioArtifact := storage.NewArtifactStore(minioClient, cfg.ArtifactBucket)
			sourceStore = minioSource
			storageProbe = func(probeCtx context.Context) bool { return storage.AllReady(probeCtx, minioSource, minioArtifact) }
			artifactStore = minioArtifact
		}
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	tokenizer, err := chunking.NewCL100K(cfg.TokenizerFile)
	if err != nil {
		cleanup()
		return nil, err
	}
	processingFactory, err := application.NewProcessingFactory(
		tokenizer,
		artifactStore,
		chunking.Policy{
			MaximumTokens: 800,
			OverlapTokens: 120,
			MaximumChunks: cfg.MaximumChunks,
		},
		artifact.Limits{
			ChunksPerShard:       256,
			MaximumShardBytes:    4 << 20,
			MaximumManifestBytes: int(cfg.MaximumManifestBytes),
		},
	)
	if err != nil {
		cleanup()
		return nil, err
	}
	events, err := transport.NewProtoEventFactory(newID)
	if err != nil {
		cleanup()
		return nil, err
	}
	workerID, err := newID()
	if err != nil {
		cleanup()
		return nil, errors.New("worker identity unavailable")
	}
	repo := repository.NewPostgres(pool)
	recorder := &metrics.Recorder{}
	pdfExtractor := extractor.NewPoppler(
		cfg.PDFInfoPath,
		cfg.PDFTextPath,
		extractor.Limits{
			MaximumPages:          cfg.MaximumPages,
			MaximumPageBytes:      cfg.MaximumPageBytes,
			MaximumExtractedBytes: cfg.MaximumExtractedBytes,
		},
		nil,
	)
	processor, err := application.NewProcessor(
		repo,
		sourceStore,
		pdfExtractor,
		processingFactory,
		events,
		newID,
		time.Now,
		workerID,
		application.Config{
			MaximumSourceBytes:    cfg.MaximumSourceBytes,
			MaximumTemporaryBytes: cfg.MaximumTemporaryBytes,
			TemporaryDirectory:    cfg.TemporaryDirectory,
			ProcessingTimeout:     cfg.ProcessingTimeout,
			JobLease:              cfg.JobLease,
			MaximumAttempts:       cfg.MaximumAttempts,
			ConfigDigest:          processingFactory.ConfigDigest(),
			Observer:              recorder,
		},
	)
	if err != nil {
		cleanup()
		return nil, err
	}
	publisher := transport.NewReconnectingPublisher(cfg.RabbitURI)
	outbox, err := transport.NewOutboxWorker(repo, publisher, cfg.ResultExchange, cfg.OutboxInterval)
	if err != nil {
		cleanup()
		return nil, err
	}
	cleaner, err := artifact.NewCleaner(repo, artifactStore, cfg.CleanupInterval, cfg.OrphanGracePeriod)
	if err != nil {
		cleanup()
		return nil, err
	}
	return &Runtime{
		Config:       cfg,
		Processor:    processor,
		Repository:   repo,
		Outbox:       outbox,
		Publisher:    publisher,
		Cleaner:      cleaner,
		Metrics:      recorder,
		Diagnostics:  diagnostic.New(nil),
		pool:         pool,
		publisher:    publisher,
		storageProbe: storageProbe,
	}, nil
}

func (r *Runtime) Close() {
	_ = r.publisher.Close()
	r.pool.Close()
}

func (r *Runtime) DatabaseReady(ctx context.Context) bool {
	return r.pool.Ping(ctx) == nil
}

func (r *Runtime) DependenciesReady(ctx context.Context) (bool, bool) {
	return r.pool.Ping(ctx) == nil, r.storageProbe != nil && r.storageProbe(ctx)
}

func (r *Runtime) Process(ctx context.Context, event application.UploadedEvent) error {
	if err := event.Validate(r.Config.MaximumSourceBytes); err != nil {
		return err
	}
	r.Diagnostics.ProcessingStarted(event.EventID, event.BookID)
	err := r.Processor.Process(ctx, event)
	if err == nil {
		r.Metrics.Processed()
		r.Diagnostics.ProcessingCompleted(event.EventID, event.BookID)
		return nil
	}
	if errors.Is(err, application.ErrProcessingDeferred) {
		r.Metrics.Deferred()
		return err
	}
	r.Metrics.Failed()
	r.Diagnostics.ProcessingFailed(event.EventID, event.BookID, application.FailureReason(err))
	return err
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:]), nil
}

type minIOConfig struct {
	endpoint, accessKey, secretKey, caFile string
	insecure                               bool
}

func newMinIOClient(cfg minIOConfig) (*minio.Client, error) {
	transportValue := http.DefaultTransport.(*http.Transport).Clone()
	transportValue.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.caFile != "" {
		contents, err := os.ReadFile(cfg.caFile) // #nosec G304 -- trusted operator path.
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
	client, err := minio.New(cfg.endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(cfg.accessKey, cfg.secretKey, ""),
		Secure:    !cfg.insecure,
		Transport: transportValue,
	})
	if err != nil {
		return nil, errors.New("object storage configuration invalid")
	}
	return client, nil
}
