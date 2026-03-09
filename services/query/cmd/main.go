// Command query starts the raglibrarian query service.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/services/query"
	"github.com/belLena81/raglibrarian/services/query/handler"
	"github.com/belLena81/raglibrarian/services/query/repository"
	"github.com/belLena81/raglibrarian/services/query/usecase"
)

func main() {
	log := logger.Must("query")
	// Flush any buffered log entries before the process exits.
	defer func() { _ = log.Sync() }()

	if err := run(log); err != nil {
		log.Fatal("service exited with error", zap.Error(err))
	}
}

func run(log *zap.Logger) error {
	addr := envOrDefault("QUERY_ADDR", ":8080")

	// Wiring: infrastructure → use case → handler → router.
	// Each layer depends only on its immediate neighbour via an interface.
	repo := repository.NewStubQueryRepository()
	svc := usecase.NewQueryService(repo)
	qh := handler.NewQueryHandler(svc, log)
	router := query.NewRouter(qh, log)

	srv := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown on SIGINT / SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		log.Info("query service starting", zap.String("addr", addr))
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return err
	case sig := <-quit:
		log.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		return err
	}

	log.Info("query service stopped gracefully")
	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
