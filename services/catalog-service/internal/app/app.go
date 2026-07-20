package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/belLena81/raglibrarian/pkg/grpcauth"
	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/catalog-service/config"
	"github.com/belLena81/raglibrarian/services/catalog-service/diagnostic"
	cataloggrpc "github.com/belLena81/raglibrarian/services/catalog-service/grpc"
	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
	"github.com/belLena81/raglibrarian/services/catalog-service/internal/metrics"
	"github.com/belLena81/raglibrarian/services/catalog-service/outbox"
	catalogprocessing "github.com/belLena81/raglibrarian/services/catalog-service/processing"
	"github.com/belLena81/raglibrarian/services/catalog-service/repository"
)

// Run composes and manages the Catalog process lifecycle.
func Run(ctx context.Context, cfg config.Config, diagnostics *diagnostic.Recorder) error {
	if diagnostics == nil {
		return errors.New("catalog diagnostics are required")
	}
	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		return fmt.Errorf("database connection: %w", err)
	}
	defer pool.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = pool.Ping(pingCtx); err != nil {
		return fmt.Errorf("database unavailable: %w", err)
	}
	minioClient, minioTransport, err := newMinIOClient(cfg)
	if err != nil {
		return fmt.Errorf("object storage configuration: %w", err)
	}
	defer minioTransport.CloseIdleConnections()
	readiness := catalogReadiness{pool: pool, objects: minioClient, bucket: cfg.MinIOBucket}
	if err = readiness.CheckReady(pingCtx); err != nil {
		return fmt.Errorf("object storage unavailable: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = listener.Close() }()
	metricsListener, err := net.Listen("tcp", cfg.MetricsAddress)
	if err != nil {
		return fmt.Errorf("metrics listen: %w", err)
	}
	defer func() { _ = metricsListener.Close() }()
	credentials, err := internaltls.ServerCredentials(cfg.TLS)
	if err != nil {
		return fmt.Errorf("TLS: %w", err)
	}
	if err = process.DropPrivileges(cfg.RunAs); err != nil {
		return err
	}
	server := grpc.NewServer(
		grpc.Creds(credentials),
		grpc.UnaryInterceptor(grpcauth.UnaryServerInterceptor(grpcauth.Policy{Service: "catalog.v1.CatalogService", DNSName: "edge-api"})),
		grpc.StreamInterceptor(grpcauth.StreamServerInterceptor(grpcauth.Policy{Service: "catalog.v1.CatalogService", DNSName: "edge-api"})),
	)
	outboxWake := make(chan struct{}, 1)
	wakeOutbox := func() {
		select {
		case outboxWake <- struct{}{}:
		default:
		}
	}
	bookRepository := repository.NewPostgresBookRepository(pool, wakeOutbox)
	objects := repository.NewMinIOObjectStore(minioClient, cfg.MinIOBucket)
	service := catalog.NewServiceWithOptions(bookRepository, objects, catalog.ServiceOptions{
		MaxBytes:          cfg.MaxUploadBytes,
		UploadConcurrency: cfg.UploadConcurrency,
	})
	catalogv1.RegisterCatalogServiceServer(server, cataloggrpc.NewServer(service, diagnostics, readiness))
	healthServer := health.NewServer()
	recorder := metrics.New(diagnostics)
	updateHealth := func() {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer probeCancel()
		postgresReady := pool.Ping(probeCtx) == nil
		_, minioErr := minioClient.StatObject(probeCtx, cfg.MinIOBucket, "originals/.readiness", minio.StatObjectOptions{})
		minioReady := minioErr == nil || minio.ToErrorResponse(minioErr).Code == "NoSuchKey"
		recorder.SetReadiness(postgresReady, minioReady)
		state := grpc_health_v1.HealthCheckResponse_NOT_SERVING
		if postgresReady && minioReady {
			state = grpc_health_v1.HealthCheckResponse_SERVING
		}
		healthServer.SetServingStatus("", state)
	}
	updateHealth()
	grpc_health_v1.RegisterHealthServer(server, healthServer)
	publisher := outbox.NewReconnectingPublisher(cfg.RabbitURI)
	defer func() { _ = publisher.Close() }()
	metricsServer := &http.Server{
		Handler:           recorder.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Add(6)
	go func() {
		defer workers.Done()
		outbox.RunWithWake(workerCtx, bookRepository, publisher, recorder, outboxWake)
	}()
	go func() {
		defer workers.Done()
		processingService := catalog.NewProcessingService(bookRepository, nil, nil)
		catalogprocessing.Run(workerCtx, cfg.IngestionRabbitURI, processingService, diagnostics)
	}()
	go func() {
		defer workers.Done()
		processingService := catalog.NewProcessingService(bookRepository, nil, nil)
		catalogprocessing.RunQueue(workerCtx, cfg.RetrievalRabbitURI, catalogprocessing.RetrievalQueue, processingService, diagnostics)
	}()
	go func() {
		defer workers.Done()
		reconciler := catalog.NewReconciler(bookRepository, objects, cfg.OrphanGracePeriod, recorder)
		catalog.RunReconciliation(workerCtx, reconciler, cfg.ReconcileInterval)
	}()
	go func() {
		defer workers.Done()
		runHealthUpdates(workerCtx, time.Second, updateHealth)
	}()
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			probeCtx, probeCancel := context.WithTimeout(workerCtx, 2*time.Second)
			backlog, backlogErr := bookRepository.OutboxBacklog(probeCtx, time.Now().UTC())
			probeCancel()
			if backlogErr == nil {
				recorder.SetOutboxBacklog(backlog.Pending, backlog.OldestAgeSecond)
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	metricsErrCh := make(chan error, 1)
	go func() { metricsErrCh <- metricsServer.Serve(metricsListener) }()
	defer func() {
		healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
		cancelWorkers()
		gracefulStop(server, 10*time.Second)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = metricsServer.Shutdown(shutdownCtx)
		workers.Wait()
	}()
	select {
	case err = <-errCh:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	case err = <-metricsErrCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("metrics server: %w", err)
	case <-ctx.Done():
		return nil
	}
}

type catalogReadiness struct {
	pool    *pgxpool.Pool
	objects *minio.Client
	bucket  string
}

func (r catalogReadiness) CheckReady(ctx context.Context) error {
	if err := r.pool.Ping(ctx); err != nil {
		return err
	}
	_, err := r.objects.StatObject(ctx, r.bucket, "originals/.readiness", minio.StatObjectOptions{})
	if err != nil && minio.ToErrorResponse(err).Code != "NoSuchKey" {
		return err
	}
	return nil
}
