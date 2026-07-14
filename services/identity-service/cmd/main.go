package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net"
	"os"
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

	server := grpc.NewServer(grpc.Creds(creds))
	authService := usecase.NewAuthService(
		repository.NewPostgresUserRepository(pool),
		signer,
	)
	identityv1.RegisterIdentityServiceServer(server, identitygrpc.NewServer(authService))

	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, healthServer)

	log.Info("identity service starting")
	if err = server.Serve(listener); err != nil {
		log.Fatal("server exited", zap.Error(err))
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
