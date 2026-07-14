package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/logger"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	identitygrpc "github.com/belLena81/raglibrarian/services/identity-service/grpc"
	"github.com/belLena81/raglibrarian/services/identity-service/repository"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase"
)

func main() {
	log := logger.Must("identity-service")
	defer func() { _ = log.Sync() }()

	dsn, keyHex := os.Getenv("IDENTITY_POSTGRES_DSN"), os.Getenv("IDENTITY_SIGNING_KEY")
	if dsn == "" || keyHex == "" {
		log.Fatal("IDENTITY_POSTGRES_DSN and IDENTITY_SIGNING_KEY are required")
	}

	key, err := hex.DecodeString(keyHex)
	if err != nil {
		log.Fatal("invalid IDENTITY_SIGNING_KEY", zap.Error(err))
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatal("database connection failed", zap.Error(err))
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = pool.Ping(ctx); err != nil {
		log.Fatal("database unavailable", zap.Error(err))
	}

	signer, err := auth.NewSigner(key, 15*time.Minute)
	if err != nil {
		log.Fatal("invalid auth configuration", zap.Error(err))
	}

	listener, err := net.Listen("tcp", env("IDENTITY_GRPC_ADDR", ":50051"))
	if err != nil {
		log.Fatal("listen failed", zap.Error(err))
	}

	creds, err := serverCredentials("IDENTITY_TLS_CERT_FILE", "IDENTITY_TLS_KEY_FILE")
	if err != nil {
		log.Fatal("invalid mTLS configuration", zap.Error(err))
	}
	if err = dropPrivileges(); err != nil {
		log.Fatal("failed to drop process privileges", zap.Error(err))
	}

	server := grpc.NewServer(grpc.Creds(creds))
	sessionRepository := repository.NewPostgresSessionRepository(pool)
	bcryptConcurrency, err := strconv.Atoi(env("IDENTITY_BCRYPT_CONCURRENCY", "4"))
	if err != nil || bcryptConcurrency < 1 {
		log.Fatal("IDENTITY_BCRYPT_CONCURRENCY must be a positive integer")
	}
	authService := usecase.NewAuthService(
		repository.NewPostgresUserRepository(pool),
		sessionRepository,
		signer,
		30*24*time.Hour,
		bcryptConcurrency,
	)
	identityv1.RegisterIdentityServiceServer(server, identitygrpc.NewServer(authService))

	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, healthServer)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	go monitorDatabaseHealth(backgroundCtx, pool, healthServer)
	go cleanupExpiredSessions(backgroundCtx, sessionRepository, log)

	errCh := make(chan error, 1)
	log.Info("identity service starting")
	go func() {
		errCh <- server.Serve(listener)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err = <-errCh:
		if !errors.Is(err, grpc.ErrServerStopped) {
			log.Fatal("server exited", zap.Error(err))
		}
	case <-quit:
		stopBackground()
		healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
		stopped := make(chan struct{})
		go func() {
			server.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			server.Stop()
		}
	}
}

func dropPrivileges() error {
	if os.Geteuid() != 0 {
		return nil
	}
	if err := syscall.Setgroups([]int{}); err != nil {
		return err
	}
	if err := syscall.Setgid(65532); err != nil {
		return err
	}
	return syscall.Setuid(65532)
}

func monitorDatabaseHealth(ctx context.Context, pool *pgxpool.Pool, healthServer *health.Server) {
	check := func() {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		status := grpc_health_v1.HealthCheckResponse_SERVING
		if pool.Ping(pingCtx) != nil {
			status = grpc_health_v1.HealthCheckResponse_NOT_SERVING
		}
		healthServer.SetServingStatus("", status)
	}
	check()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func cleanupExpiredSessions(ctx context.Context, sessions repository.SessionRepository, log *zap.Logger) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		deleted, err := sessions.CleanupExpired(cleanupCtx, time.Now().UTC())
		if err != nil {
			log.Warn("expired session cleanup failed", zap.Error(err))
			return
		}
		if deleted > 0 {
			log.Info("expired sessions removed", zap.Int64("count", deleted))
		}
	}
	cleanup()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}

func serverCredentials(certFile, keyFile string) (credentials.TransportCredentials, error) {
	ca, err := os.ReadFile(os.Getenv("INTERNAL_TLS_CA_FILE")) // #nosec G703 -- operator-controlled runtime configuration
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, os.ErrInvalid
	}
	cert, err := tls.LoadX509KeyPair(os.Getenv(certFile), os.Getenv(keyFile))
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}

	return credentials.NewTLS(tlsConfig), nil
}
