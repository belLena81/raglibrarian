package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/bootstrap"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("ingestion cleanup job stopped", "reason", "runtime_failure")
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	if os.Geteuid() == 0 {
		return errors.New("ingestion cleanup job must be non-root")
	}
	cfg, err := config.LoadCleanupContext(ctx)
	if err != nil {
		return err
	}
	runtime, err := bootstrap.NewCleanup(ctx, cfg)
	if err != nil {
		return err
	}
	defer runtime.Close()
	return runtime.Cleaner.RunOnce(ctx)
}
