package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/services/edge-api/config"
	"github.com/belLena81/raglibrarian/services/edge-api/internal/app"
)

func main() {
	log := logger.Must("edge-api")
	defer func() { _ = log.Sync() }()
	cfg, err := config.Load()
	if err != nil {
		log.Fatal("service.start.failed")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err = app.Run(ctx, cfg, log); err != nil {
		log.Fatal("service.run.failed")
	}
}
