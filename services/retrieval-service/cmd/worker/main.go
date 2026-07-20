package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/worker"
)

func main() {
	configuration, err := config.LoadWorker()
	if err != nil {
		worker.LogFailure()
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	runtime, err := worker.New(ctx, configuration)
	if err != nil {
		worker.LogFailure()
		os.Exit(1)
	}
	if err = runtime.Run(ctx); err != nil {
		worker.LogFailure()
		os.Exit(1)
	}
}
