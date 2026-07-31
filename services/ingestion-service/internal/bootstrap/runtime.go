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

	"github.com/belLena81/raglibrarian/pkg/indexprofile"
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

var verifyParserSandbox = extractor.VerifySandbox

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

func NewDispatcher(ctx context.Context, cfg config.DispatcherConfig) (*DispatcherRuntime, error) {
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
	brokerPolicy := transport.BrokerPolicy{
		DialTimeout:    cfg.RabbitDialTimeout,
		Heartbeat:      cfg.RabbitHeartbeat,
		PublishTimeout: cfg.RabbitPublishTimeout,
	}
	chunkPolicy := chunking.Policy{
		MaximumTokens: cfg.ChunkMaximumTokens,
		OverlapTokens: cfg.ChunkOverlapTokens,
		TargetPages:   cfg.ChunkTargetPages,
		MaximumPages:  cfg.ChunkMaximumPages,
		MaximumChunks: 1,
	}
	publisher := transport.NewReconnectingPublisher(cfg.RabbitURI, brokerPolicy)
	outbox, err := transport.NewOutboxWorker(repository.NewPostgresWithProfile(pool, chunkPolicy, repository.Policy{
		RetryDispatchDelay:   cfg.RetryDispatchDelay,
		OutboxRetryBaseDelay: cfg.OutboxRetryBaseDelay,
		OutboxRetryMaxDelay:  cfg.OutboxRetryMaxDelay,
	}), publisher, cfg.ResultExchange, cfg.OutboxInterval, transport.OutboxPolicy{
		Lease:                      cfg.OutboxLease,
		PublishTimeout:             cfg.RabbitPublishTimeout,
		RetryExchange:              cfg.RetryExchange,
		UploadFirstRetryRoute:      cfg.UploadFirstRetryRoute,
		UploadSecondRetryRoute:     cfg.UploadSecondRetryRoute,
		UploadSubsequentRetryRoute: cfg.UploadSubsequentRetryRoute,
		FirstRetryDelay:            cfg.FirstRetryDelay,
		SecondRetryDelay:           cfg.SecondRetryDelay,
	}, diagnostic.New(nil))
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
		artifactStore, err = storage.NewAWSArtifactStoreWithPolicy(client, cfg.ArtifactBucket, cfg.KMSKeyARN, cfg.ArtifactVersionCleanupPasses)
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
	chunkPolicy := chunking.Policy{
		MaximumTokens: cfg.ChunkMaximumTokens,
		OverlapTokens: cfg.ChunkOverlapTokens,
		TargetPages:   cfg.ChunkTargetPages,
		MaximumPages:  cfg.ChunkMaximumPages,
		MaximumChunks: 1,
	}
	cleaner, err := artifact.NewCleaner(repository.NewPostgresWithProfile(pool, chunkPolicy, repository.Policy{
		RetryDispatchDelay:   cfg.RetryDispatchDelay,
		OutboxRetryBaseDelay: cfg.OutboxRetryBaseDelay,
		OutboxRetryMaxDelay:  cfg.OutboxRetryMaxDelay,
	}), artifactStore, cfg.CleanupInterval, cfg.OrphanGracePeriod)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &CleanupRuntime{Cleaner: cleaner, pool: pool}, nil
}

func (r *CleanupRuntime) Close() { r.pool.Close() }

func New(ctx context.Context, cfg config.Config) (*Runtime, error) {
	if err := verifyParserSandbox(ctx); err != nil {
		return nil, err
	}
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
			awsArtifact, err = storage.NewAWSArtifactStoreWithPolicy(client, cfg.ArtifactBucket, cfg.KMSKeyARN, cfg.ArtifactVersionCleanupPasses)
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
	chunkPolicy := chunking.Policy{
		MaximumTokens: cfg.ChunkMaximumTokens,
		OverlapTokens: cfg.ChunkOverlapTokens,
		TargetPages:   cfg.ChunkTargetPages,
		MaximumPages:  cfg.ChunkMaximumPages,
		MaximumChunks: cfg.MaximumChunks,
	}
	selectionProfile := application.ContentSelectionProfile{Mode: application.ContentSelectionDisabled}
	if cfg.RuntimeBackend == "local" {
		selectionProfile = application.ContentSelectionProfile{
			Mode:                 application.ContentSelectionMode(cfg.ContentSelectionMode),
			PolicyVersion:        cfg.ContentSelectionPolicyVersion,
			ParserVersion:        cfg.ContentSelectionParserVersion,
			ModelSHA256:          cfg.ContentSelectionModelDigest,
			MinimumSignals:       cfg.ContentSelectionMinimumSignals,
			MaximumRanges:        cfg.ContentSelectionMaximumRanges,
			MaximumExcludedRatio: cfg.ContentSelectionMaximumExcludedRatio,
		}
	}
	if err = selectionProfile.Validate(); err != nil {
		cleanup()
		return nil, err
	}
	if selectionProfile.Mode != application.ContentSelectionDisabled {
		selectionDigest := selectionProfile.Digest()
		chunkPolicy.IdentityProfile = hex.EncodeToString(selectionDigest[:])
	}
	processingFactory, err := application.NewProcessingFactoryWithSelection(
		tokenizer,
		artifactStore,
		chunkPolicy,
		artifact.Limits{
			ChunksPerShard:       cfg.ArtifactChunksPerShard,
			MaximumShardBytes:    int(cfg.ArtifactMaximumShardBytes),
			MaximumManifestBytes: int(cfg.MaximumManifestBytes),
		},
		selectionProfile,
	)
	if err != nil {
		cleanup()
		return nil, err
	}
	events, err := transport.NewProtoEventFactoryWithSelection(newID, chunkPolicy, selectionProfile)
	if err != nil {
		cleanup()
		return nil, err
	}
	workerID, err := newID()
	if err != nil {
		cleanup()
		return nil, errors.New("worker identity unavailable")
	}
	repo := repository.NewPostgresWithProfile(pool, chunkPolicy, repository.Policy{
		RetryDispatchDelay:   cfg.RetryDispatchDelay,
		OutboxRetryBaseDelay: cfg.OutboxRetryBaseDelay,
		OutboxRetryMaxDelay:  cfg.OutboxRetryMaxDelay,
	})
	recorder := &metrics.Recorder{}
	diagnosticsLogger := diagnostic.New(nil)
	pdfExtractor := extractor.NewPopplerWithOptions(
		cfg.PDFInfoPath,
		cfg.PDFTextPath,
		extractor.Limits{
			MaximumPages:          cfg.MaximumPages,
			MaximumPageBytes:      cfg.MaximumPageBytes,
			MaximumExtractedBytes: cfg.MaximumExtractedBytes,
		},
		nil,
		cfg.DebugDumpPDFTextDirectory,
	)
	epubExtractor := extractor.NewEPUB(
		cfg.EPUBParserPath,
		extractor.Limits{
			MaximumPages:          cfg.MaximumPages,
			MaximumPageBytes:      cfg.MaximumPageBytes,
			MaximumExtractedBytes: cfg.MaximumExtractedBytes,
		},
		extractor.EPUBArchiveLimits{
			MaximumEntries:       cfg.EPUBMaximumEntries,
			MaximumSpineItems:    cfg.EPUBMaximumSpineItems,
			MaximumEntryBytes:    cfg.EPUBMaximumEntryBytes,
			MaximumExpandedBytes: cfg.EPUBMaximumExpandedBytes,
			MaximumTextBytes:     cfg.EPUBMaximumTextBytes,
		},
		nil,
	)
	pdfExtractionVersion := extractor.ExtractionVersion
	epubExtractionVersion := extractor.EPUBExtractionVersion
	if selectionProfile.Mode != application.ContentSelectionDisabled {
		pdfExtractionVersion = indexprofile.ExtractionPDFFiltered
		epubExtractionVersion = indexprofile.ExtractionEPUBFiltered
	}
	extractors, err := application.NewFormatExtractors(
		application.ExtractionAdapter{
			MediaType: application.MediaTypePDF,
			Extension: ".pdf",
			Version:   pdfExtractionVersion,
			Extractor: pdfExtractor,
		},
		application.ExtractionAdapter{
			MediaType: application.MediaTypeEPUB,
			Extension: ".epub",
			Version:   epubExtractionVersion,
			Extractor: epubExtractor,
		},
	)
	if err != nil {
		cleanup()
		return nil, err
	}
	processor, err := application.NewProcessor(
		repo,
		sourceStore,
		extractors,
		processingFactory,
		events,
		newID,
		time.Now,
		workerID,
		application.Config{
			MaximumSourceBytes:     cfg.MaximumSourceBytes,
			MaximumTemporaryBytes:  cfg.MaximumTemporaryBytes,
			TemporaryDirectory:     cfg.TemporaryDirectory,
			ProcessingTimeout:      cfg.ProcessingTimeout,
			PersistenceTimeout:     cfg.PersistenceTimeout,
			ArtifactAbortTimeout:   cfg.ArtifactAbortTimeout,
			JobLease:               cfg.JobLease,
			MaximumAttempts:        cfg.MaximumAttempts,
			FirstRetryDelay:        cfg.FirstRetryDelay,
			SecondRetryDelay:       cfg.SecondRetryDelay,
			SubsequentRetryDelay:   cfg.SubsequentRetryDelay,
			Observer:               recorder,
			Diagnostics:            diagnosticsLogger,
			DecodeUploaded:         transport.DecodeUploaded,
			DecodeContentSelection: transport.DecodeContentSelectionCompleted,
		},
	)
	if err != nil {
		cleanup()
		return nil, err
	}
	brokerPolicy := transport.BrokerPolicy{
		MaximumAttempts:              cfg.MaximumAttempts,
		RetryExchange:                cfg.RetryExchange,
		UploadFirstRetryRoute:        cfg.UploadFirstRetryRoute,
		UploadSecondRetryRoute:       cfg.UploadSecondRetryRoute,
		UploadSubsequentRetryRoute:   cfg.UploadSubsequentRetryRoute,
		DeletionFirstRetryRoute:      cfg.DeletionFirstRetryRoute,
		DeletionSecondRetryRoute:     cfg.DeletionSecondRetryRoute,
		DeletionSubsequentRetryRoute: cfg.DeletionSubsequentRetryRoute,
		DialTimeout:                  cfg.RabbitDialTimeout,
		Heartbeat:                    cfg.RabbitHeartbeat,
		PublishTimeout:               cfg.RabbitPublishTimeout,
		FirstRetryDelay:              cfg.FirstRetryDelay,
		SecondRetryDelay:             cfg.SecondRetryDelay,
		SubsequentRetryDelay:         cfg.SubsequentRetryDelay,
	}
	publisher := transport.NewReconnectingPublisher(cfg.RabbitURI, brokerPolicy)
	outbox, err := transport.NewOutboxWorker(repo, publisher, cfg.ResultExchange, cfg.OutboxInterval, transport.OutboxPolicy{
		Lease:                      cfg.OutboxLease,
		PublishTimeout:             cfg.RabbitPublishTimeout,
		RetryExchange:              cfg.RetryExchange,
		UploadFirstRetryRoute:      cfg.UploadFirstRetryRoute,
		UploadSecondRetryRoute:     cfg.UploadSecondRetryRoute,
		UploadSubsequentRetryRoute: cfg.UploadSubsequentRetryRoute,
		FirstRetryDelay:            cfg.FirstRetryDelay,
		SecondRetryDelay:           cfg.SecondRetryDelay,
	}, diagnosticsLogger)
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
		Diagnostics:  diagnosticsLogger,
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
		retryAt := time.Time{}
		var deferred application.DeferredError
		if errors.As(err, &deferred) {
			retryAt = deferred.RetryAt
		}
		r.Diagnostics.ProcessingDeferred(event.EventID, event.BookID, application.FailureReason(err), application.FailureDetail(err), retryAt)
		return err
	}
	r.Metrics.Failed()
	r.Diagnostics.ProcessingFailed(event.EventID, event.BookID, application.FailureReason(err), application.FailureDetail(err))
	return err
}

func (r *Runtime) ProcessDeletion(ctx context.Context, event application.DeletionEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if err := r.Processor.ProcessDeletion(ctx, event); err != nil {
		return err
	}
	r.Cleaner.WakeDeletionCleanup()
	return nil
}

func (r *Runtime) ProcessContentSelection(ctx context.Context, event application.ContentSelectionResult) error {
	return r.Processor.ProcessContentSelection(ctx, event)
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
