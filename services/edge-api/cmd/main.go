// Command edge-api exposes the public HTTP API and delegates identity writes to gRPC.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/config"
	"github.com/belLena81/raglibrarian/pkg/logger"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	edgeapi "github.com/belLena81/raglibrarian/services/edge-api"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/identityclient"
	queryrepo "github.com/belLena81/raglibrarian/services/edge-api/repository"
	"github.com/belLena81/raglibrarian/services/edge-api/usecase"
)

func main() {
	log := logger.Must("edge-api")
	defer func() { _ = log.Sync() }()
	if err := run(log); err != nil {
		log.Fatal("service exited", zap.Error(err))
	}
}

func run(log *zap.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	verifier, err := auth.NewVerifier(cfg.VerifyKey)
	if err != nil {
		return err
	}
	address := os.Getenv("IDENTITY_GRPC_ADDR")
	if address == "" {
		address = "identity-service:50051"
	}
	creds, err := clientCredentials()
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	authHandler := handler.NewAuthHandler(identityclient.New(identityv1.NewIdentityServiceClient(conn)), log)
	queryHandler := handler.NewQueryHandler(usecase.NewQueryService(queryrepo.NewStubQueryRepository()), log)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           edgeapi.NewRouter(queryHandler, authHandler, verifier, log),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-quit:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

func clientCredentials() (credentials.TransportCredentials, error) {
	ca, err := os.ReadFile(os.Getenv("INTERNAL_TLS_CA_FILE")) // #nosec G703 -- operator-controlled runtime configuration
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid internal CA")
	}
	cert, err := tls.LoadX509KeyPair(os.Getenv("EDGE_TLS_CERT_FILE"), os.Getenv("EDGE_TLS_KEY_FILE"))
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		ServerName:   "identity-service",
	}), nil
}
