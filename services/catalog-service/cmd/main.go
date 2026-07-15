package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/services/catalog-service/config"
	"github.com/belLena81/raglibrarian/services/catalog-service/internal/app"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err = app.Run(ctx, cfg); err != nil {
		panic(err)
	}
}
