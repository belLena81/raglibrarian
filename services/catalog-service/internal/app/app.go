package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

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
)

// Run composes and manages the Catalog process lifecycle.
func Run(ctx context.Context, cfg config.Config) error {
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
	service := catalog.NewService(catalog.NewMemoryRepository(), catalog.NewMemoryObjectStore(), 0)
	catalogv1.RegisterCatalogServiceServer(server, cataloggrpc.NewServer(service))
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, healthServer)
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
