package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/bootstrap"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/transport"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil && !errors.Is(err, context.Canceled) {
		logRunError(logger, err)
		os.Exit(1)
	}
}

func logRunError(logger *slog.Logger, err error) {
	logger.Error("ingestion worker stopped", "reason", "runtime_failure", "error", err)
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err = process.DropPrivileges(cfg.RunAs); err != nil {
		return err
	}
	runtime, err := bootstrap.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer runtime.Close()
	connection, err := transport.DialConsumer(ctx, cfg.RabbitURI)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	channel, err := connection.Channel()
	if err != nil {
		return errors.New("broker channel unavailable")
	}
	defer func() { _ = channel.Close() }()
	consumer, err := transport.NewConsumer(channel, cfg.Queue, cfg.WorkConcurrency, runtime, runtime.Publisher)
	if err != nil {
		return err
	}
	initialProbeCtx, cancelInitialProbe := context.WithTimeout(ctx, cfg.WorkerReadinessProbeTimeout)
	postgresReady, storageReady := runtime.DependenciesReady(initialProbeCtx)
	cancelInitialProbe()
	runtime.Metrics.SetReadiness(postgresReady, storageReady, !connection.IsClosed())
	readinessDone := make(chan struct{})
	defer close(readinessDone)
	go func() {
		ticker := time.NewTicker(cfg.WorkerReadinessRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-readinessDone:
				return
			case <-ticker.C:
				probeCtx, cancel := context.WithTimeout(ctx, cfg.WorkerReadinessProbeTimeout)
				postgresReady, storageReady := runtime.DependenciesReady(probeCtx)
				cancel()
				runtime.Metrics.SetReadiness(postgresReady, storageReady, !connection.IsClosed())
			}
		}
	}()
	metricsServer := &http.Server{Addr: cfg.MetricsAddress, Handler: runtime.Metrics.Handler(), ReadHeaderTimeout: cfg.WorkerMetricsReadHeaderTimeout}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- metricsServer.ListenAndServe() }()
	outboxErrors := make(chan error, 1)
	go func() { outboxErrors <- runtime.Outbox.Run(ctx) }()
	cleanupErrors := make(chan error, 1)
	go func() { cleanupErrors <- runtime.Cleaner.Run(ctx) }()
	consumerErrors := make(chan error, 1)
	go func() { consumerErrors <- consumer.Run(ctx, cfg.WorkConcurrency) }()
	logger.Info("ingestion worker started",
		"chunking_version", chunking.ChunkingVersion,
		"structure_version", chunking.StructureVersion,
		"chunk_maximum_tokens", cfg.ChunkMaximumTokens,
		"chunk_overlap_tokens", cfg.ChunkOverlapTokens,
		"chunk_target_pages", cfg.ChunkTargetPages,
		"chunk_maximum_pages", cfg.ChunkMaximumPages,
		"parser_sandbox_memory_bytes", cfg.ParserSandboxMemoryBytes,
	)
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.WorkerMetricsShutdownTimeout)
		defer cancel()
		_ = metricsServer.Shutdown(shutdownCtx)
		return ctx.Err()
	case err = <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err = <-outboxErrors:
		return err
	case err = <-cleanupErrors:
		return err
	case err = <-consumerErrors:
		return err
	}
}
