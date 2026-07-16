package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/services/identity-service/config"
	"github.com/belLena81/raglibrarian/services/identity-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/identity-service/internal/app"
)

func main() {
	log := logger.Must("identity-service")
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
