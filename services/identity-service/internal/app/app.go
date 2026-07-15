package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/grpcauth"
	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	"github.com/belLena81/raglibrarian/services/identity-service/config"
	identitygrpc "github.com/belLena81/raglibrarian/services/identity-service/grpc"
	"github.com/belLena81/raglibrarian/services/identity-service/password"
	"github.com/belLena81/raglibrarian/services/identity-service/repository"
	identitytoken "github.com/belLena81/raglibrarian/services/identity-service/token"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase"
)

type systemClock struct{}

// Now implements the application clock with UTC-normalized caller usage.
func (systemClock) Now() time.Time { return time.Now() }

// Run composes and manages the Identity process lifecycle.
func Run(ctx context.Context, cfg config.Config, log *zap.Logger) error {
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
	signer, err := auth.NewSigner(cfg.SigningKey, 15*time.Minute)
	if err != nil {
		return err
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
		grpc.UnaryInterceptor(grpcauth.UnaryServerInterceptor(grpcauth.Policy{Service: "identity.v1.IdentityService", DNSName: "edge-api"})),
	)
	sessions := repository.NewPostgresSessionRepository(pool)
	passwords := password.NewLimitedHasher(password.BcryptHasher{}, cfg.BcryptConcurrency)
	authService := usecase.NewAuthService(repository.NewPostgresUserRepository(pool), sessions, identitytoken.NewIssuer(signer), passwords, systemClock{}, 30*24*time.Hour)
	identityv1.RegisterIdentityServiceServer(server, identitygrpc.NewServer(authService))
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, healthServer)
	backgroundCtx, stopBackground := context.WithCancel(ctx)
	defer stopBackground()
	go monitorDatabaseHealth(backgroundCtx, pool, healthServer)
	go cleanupExpiredSessions(backgroundCtx, sessions, log)
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	select {
	case err = <-errCh:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	case <-ctx.Done():
		stopBackground()
		healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
		gracefulStop(server, 10*time.Second)
		return nil
	}
}
