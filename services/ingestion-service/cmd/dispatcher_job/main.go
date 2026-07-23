package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/bootstrap"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("ingestion dispatcher job stopped", "reason", "runtime_failure")
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadContext(ctx)
	if err != nil {
		return err
	}
	if err = process.DropPrivileges(cfg.RunAs); err != nil {
		return err
	}
	if err = config.ValidateServerlessBrokerURI(cfg.RabbitURI); err != nil {
		return err
	}
	runtime, err := bootstrap.NewDispatcher(ctx, cfg)
	if err != nil {
		return err
	}
	defer runtime.Close()
	return runtime.Outbox.PublishPending(ctx)
}
