// Command edge-api exposes the public HTTP API and delegates identity writes to gRPC.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/config"
	"github.com/belLena81/raglibrarian/pkg/logger"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	"github.com/belLena81/raglibrarian/services/edge-api"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/identityclient"
	queryrepo "github.com/belLena81/raglibrarian/services/edge-api/repository"
	"github.com/belLena81/raglibrarian/services/edge-api/usecase"
)

func main() { log := logger.Must("edge-api"); defer log.Sync(); if err := run(log); err != nil { log.Fatal("service exited", zap.Error(err)) } }

func run(log *zap.Logger) error {
	cfg, err := config.Load(); if err != nil { return err }
	issuer, err := auth.NewIssuer(cfg.AuthSecretKey, cfg.TokenTTL); if err != nil { return err }
	address := os.Getenv("IDENTITY_GRPC_ADDR"); if address == "" { address = "identity-service:50051" }
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials())); if err != nil { return err }; defer conn.Close()
	authHandler := handler.NewAuthHandler(identityclient.New(identityv1.NewIdentityServiceClient(conn)), log)
	queryHandler := handler.NewQueryHandler(usecase.NewQueryService(queryrepo.NewStubQueryRepository()), log)
	srv := &http.Server{Addr: cfg.Addr, Handler: edgeapi.NewRouter(queryHandler, authHandler, issuer, log), ReadTimeout: 10*time.Second, WriteTimeout: 30*time.Second, IdleTimeout: 60*time.Second}
	quit := make(chan os.Signal, 1); signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	errCh := make(chan error, 1); go func() { if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) { errCh <- err } }()
	select { case err := <-errCh: return err; case <-quit: }
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second); defer cancel(); return srv.Shutdown(ctx)
}
