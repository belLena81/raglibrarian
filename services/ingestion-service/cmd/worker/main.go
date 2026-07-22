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
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/transport"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("ingestion worker stopped", "reason", "runtime_failure")
		os.Exit(1)
	}
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
	initialProbeCtx, cancelInitialProbe := context.WithTimeout(ctx, time.Second)
	postgresReady, storageReady := runtime.DependenciesReady(initialProbeCtx)
	cancelInitialProbe()
	runtime.Metrics.SetReadiness(postgresReady, storageReady, !connection.IsClosed())
	readinessDone := make(chan struct{})
	defer close(readinessDone)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-readinessDone:
				return
			case <-ticker.C:
				probeCtx, cancel := context.WithTimeout(ctx, time.Second)
				postgresReady, storageReady := runtime.DependenciesReady(probeCtx)
				cancel()
				runtime.Metrics.SetReadiness(postgresReady, storageReady, !connection.IsClosed())
			}
		}
	}()
	metricsServer := &http.Server{Addr: cfg.MetricsAddress, Handler: runtime.Metrics.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- metricsServer.ListenAndServe() }()
	outboxErrors := make(chan error, 1)
	go func() { outboxErrors <- runtime.Outbox.Run(ctx) }()
	cleanupErrors := make(chan error, 1)
	go func() { cleanupErrors <- runtime.Cleaner.Run(ctx) }()
	consumerErrors := make(chan error, 1)
	go func() { consumerErrors <- consumer.Run(ctx, cfg.WorkConcurrency) }()
	logger.Info("ingestion worker started")
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
