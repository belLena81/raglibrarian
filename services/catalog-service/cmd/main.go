package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/services/catalog-service/config"
	"github.com/belLena81/raglibrarian/services/catalog-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/catalog-service/internal/app"
)

func main() {
	log := logger.Must("catalog-service")
	defer func() { _ = log.Sync() }()
	cfg, err := config.Load()
	if err != nil {
		log.Error("catalog service could not start because configuration was invalid")
		returnWithFailure()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err = app.Run(ctx, cfg, diagnostic.New(log)); err != nil {
		log.Error("catalog service stopped because a required dependency or listener was unavailable")
		returnWithFailure()
	}
}

func returnWithFailure() {
	os.Exit(1)
}
