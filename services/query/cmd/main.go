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

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/config"
	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/services/metadata/repository"
	metausecase "github.com/belLena81/raglibrarian/services/metadata/usecase"
	"github.com/belLena81/raglibrarian/services/query"
	"github.com/belLena81/raglibrarian/services/query/handler"
	queryrepo "github.com/belLena81/raglibrarian/services/query/repository"
	"github.com/belLena81/raglibrarian/services/query/usecase"
)

func main() {
	log := logger.Must("query")
	defer func() { _ = log.Sync() }()

	if err := run(log); err != nil {
		log.Fatal("service exited with error", zap.Error(err))
	}
}

func run(log *zap.Logger) error {
	// ── Config ────────────────────────────────────────────────────────────
	// Load validates all required env vars and fails fast with a precise
	// message — no silent nil pointers or zero values in production.
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// ── Database ──────────────────────────────────────────────────────────
	// pgxpool creates a bounded connection pool. The pool is safe for
	// concurrent use. We verify connectivity at startup so the service
	// refuses to accept traffic if the DB is unreachable.
	pool, err := pgxpool.New(context.Background(), cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = pool.Ping(ctx); err != nil {
		return err
	}
	log.Info("database connected")

	// ── Auth ──────────────────────────────────────────────────────────────
	issuer, err := auth.NewIssuer(cfg.AuthSecretKey, cfg.TokenTTL)
	if err != nil {
		return err
	}

	// ── Wiring ────────────────────────────────────────────────────────────
	// Infrastructure → Repository → UseCase → Handler → Router.
	// Each layer depends only on the interface of the layer below it.
	userRepo := repository.NewPostgresUserRepository(pool)
	authSvc := metausecase.NewAuthService(userRepo, issuer)
	ah := handler.NewAuthHandler(authSvc, log)

	queryRepo := queryrepo.NewStubQueryRepository()
	querySvc := usecase.NewQueryService(queryRepo)
	qh := handler.NewQueryHandler(querySvc, log)

	router := query.NewRouter(qh, ah, issuer, log)

	// ── HTTP server with graceful shutdown ────────────────────────────────
	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		log.Info("query service starting", zap.String("addr", cfg.Addr))
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

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()

	if err := srv.Shutdown(shutCtx); err != nil {
		return err
	}

	log.Info("query service stopped gracefully")
	return nil
}
