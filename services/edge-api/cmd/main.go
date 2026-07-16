package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/services/edge-api/config"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	"github.com/belLena81/raglibrarian/services/edge-api/internal/app"
)

func main() {
	log := logger.Must("edge-api")
	defer func() { _ = log.Sync() }()
	diagnostics := diagnostic.New(log)
	cfg, err := config.Load()
	if err != nil {
		diagnostics.ServiceStartFailed()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err = app.Run(ctx, cfg, diagnostics); err != nil {
		diagnostics.ServiceRunFailed()
	}
}
