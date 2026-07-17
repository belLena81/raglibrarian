package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/belLena81/raglibrarian/pkg/grpcauth"
	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/catalog-service/config"
	cataloggrpc "github.com/belLena81/raglibrarian/services/catalog-service/grpc"
	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
	"github.com/belLena81/raglibrarian/services/catalog-service/outbox"
	"github.com/belLena81/raglibrarian/services/catalog-service/repository"
)

// Run composes and manages the Catalog process lifecycle.
func Run(ctx context.Context, cfg config.Config) error {
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
	minioClient, err := minio.New(cfg.MinIOEndpoint, &minio.Options{Creds: credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, ""), Secure: false})
	if err != nil {
		return fmt.Errorf("object storage configuration: %w", err)
	}
	readiness := catalogReadiness{pool: pool, objects: minioClient, bucket: cfg.MinIOBucket}
	if err = readiness.CheckReady(pingCtx); err != nil {
		return fmt.Errorf("object storage unavailable: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = listener.Close() }()
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
	bookRepository := repository.NewPostgresBookRepository(pool)
	service := catalog.NewService(bookRepository, repository.NewMinIOObjectStore(minioClient, cfg.MinIOBucket), 0)
	catalogv1.RegisterCatalogServiceServer(server, cataloggrpc.NewServer(service, readiness))
	healthServer := health.NewServer()
	updateHealth := func() {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer probeCancel()
		state := grpc_health_v1.HealthCheckResponse_SERVING
		if readiness.CheckReady(probeCtx) != nil {
			state = grpc_health_v1.HealthCheckResponse_NOT_SERVING
		}
		healthServer.SetServingStatus("", state)
	}
	updateHealth()
	grpc_health_v1.RegisterHealthServer(server, healthServer)
	publisher := outbox.NewReconnectingPublisher(cfg.RabbitURI)
	defer func() { _ = publisher.Close() }()
	go outbox.Run(ctx, bookRepository, publisher)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				updateHealth()
			}
		}
	}()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	select {
	case err = <-errCh:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	case <-ctx.Done():
		healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
		gracefulStop(server, 10*time.Second)
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
