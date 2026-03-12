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
	metapublisher "github.com/belLena81/raglibrarian/services/metadata/publisher"
	metarepo "github.com/belLena81/raglibrarian/services/metadata/repository"
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// ── Database ──────────────────────────────────────────────────────────
	pool, err := pgxpool.New(context.Background(), cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err = pool.Ping(pingCtx); err != nil {
		return err
	}
	log.Info("database connected")

	// ── Messaging ─────────────────────────────────────────────────────────
	// The query service publishes EventBookCreated/EventBookReindexRequested
	// via the BookService when librarians add or reindex books through the
	// REST API. The same AMQP publisher used by the metadata service is wired
	// here so both services share the same event topology.
	pub, err := metapublisher.NewAMQPBookPublisher(cfg.AMQPUrl)
	if err != nil {
		return err
	}
	defer func() { _ = pub.Close() }()
	log.Info("message broker connected")

	// ── Auth ──────────────────────────────────────────────────────────────
	issuer, err := auth.NewIssuer(cfg.AuthSecretKey, cfg.TokenTTL)
	if err != nil {
		return err
	}

	// ── Wiring ────────────────────────────────────────────────────────────
	// Infrastructure → Repository/Publisher → UseCase → Handler → Router.
	userRepo := metarepo.NewPostgresUserRepository(pool)
	authSvc := metausecase.NewAuthService(userRepo, issuer)
	ah := handler.NewAuthHandler(authSvc, log)

	bookRepo := metarepo.NewPostgresBookRepository(pool)
	bookSvc := metausecase.NewBookService(bookRepo, pub)
	bh := handler.NewBookHandler(bookSvc, log)

	queryRepo := queryrepo.NewStubQueryRepository()
	querySvc := usecase.NewQueryService(queryRepo)
	qh := handler.NewQueryHandler(querySvc, log)

	router := query.NewRouter(qh, ah, bh, issuer, log)

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
