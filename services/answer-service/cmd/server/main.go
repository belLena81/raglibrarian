package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/services/answer-service/config"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/app"
)

func main() {
	configuration, err := config.Load()
	if err != nil {
		log.Print("answer service could not start because configuration was invalid")
		os.Exit(1)
	}
	application, err := app.New(configuration)
	if err != nil {
		log.Print("answer service could not initialize")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err = application.Run(ctx); err != nil {
		log.Print("answer service stopped unexpectedly")
		os.Exit(1)
	}
}
