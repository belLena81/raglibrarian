// Command metadata starts the raglibrarian metadata gRPC service.
package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/belLena81/raglibrarian/pkg/proto/metadatapb"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/belLena81/raglibrarian/pkg/config"
	"github.com/belLena81/raglibrarian/pkg/logger"
	metaGRPC "github.com/belLena81/raglibrarian/services/metadata/grpc"
	metarepo "github.com/belLena81/raglibrarian/services/metadata/repository"
	metausecase "github.com/belLena81/raglibrarian/services/metadata/usecase"
)

func main() {
	log := logger.Must("metadata")
	defer func() { _ = log.Sync() }()

	if err := run(log); err != nil {
		log.Fatal("service exited with error", zap.Error(err))
	}
}

func run(log *zap.Logger) error {
	// ── Config ────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// ── Database ──────────────────────────────────────────────────────────
	pool, err := pgxpool.New(context.Background(), cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err = pool.Ping(pingCtx); err != nil {
		return err
	}
	log.Info("database connected")

	// ── Wiring ────────────────────────────────────────────────────────────
	// Infrastructure → Repository → UseCase → gRPC server.
	bookRepo := metarepo.NewPostgresBookRepository(pool)
	bookSvc := metausecase.NewBookService(bookRepo)
	metaSrv := metaGRPC.NewMetadataServer(bookSvc)

	// ── gRPC server ───────────────────────────────────────────────────────
	// KeepaliveParams prevents idle connections from being silently dropped
	// by NAT gateways between the Lambda and this service.
	grpcSrv := grpc.NewServer(
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 30 * time.Second,
			Time:              10 * time.Second,
			Timeout:           5 * time.Second,
		}),
	)
	metadatapb.RegisterMetadataServiceServer(grpcSrv, metaSrv)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	serveErr := make(chan error, 1)
	go func() {
		log.Info("metadata gRPC service starting", zap.String("addr", cfg.GRPCAddr))
		if err := grpcSrv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return err
	case sig := <-quit:
		log.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	// GracefulStop drains in-flight RPCs before closing; 15 s matches the
	// query service's HTTP shutdown budget.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()

	stopped := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
		log.Info("metadata service stopped gracefully")
	case <-shutCtx.Done():
		grpcSrv.Stop() // force-stop if GracefulStop exceeds the deadline
		log.Warn("metadata service forced stop after deadline")
	}

	return nil
}
