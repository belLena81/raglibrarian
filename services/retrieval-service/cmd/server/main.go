package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/pkg/grpcauth"
	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/pkg/process"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	retrievalgrpc "github.com/belLena81/raglibrarian/services/retrieval-service/internal/grpc"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
)

func main() {
	configuration, err := config.Load()
	if err != nil {
		log.Print("retrieval server could not start because configuration was invalid")
		os.Exit(1)
	}
	serviceLogger := logger.Must("retrieval-service")
	transportCredentials, err := internaltls.ServerCredentials(configuration.TLS)
	if err != nil {
		log.Print("retrieval server could not load transport credentials")
		os.Exit(1)
	}
	httpClient := &http.Client{Timeout: configuration.DependencyTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	embedder, err := configureEmbedder(configuration, httpClient, serviceLogger)
	if err != nil {
		log.Print("retrieval server could not configure embedding dependency")
		os.Exit(1)
	}
	store, err := configureVectorStore(configuration, httpClient)
	if err != nil {
		log.Print("retrieval server could not configure vector dependency")
		os.Exit(1)
	}
	dsn, err := readSecret(configuration.PostgresDSNFile)
	if err != nil {
		log.Print("retrieval server could not read database credentials")
		os.Exit(1)
	}
	summaryProvider, err := configureSummaryProvider(configuration, serviceLogger)
	if err != nil {
		log.Print("retrieval server could not configure summary provider")
		os.Exit(1)
	}
	if err = process.DropPrivileges(configuration.RunAs); err != nil {
		log.Print("retrieval server could not reduce process privileges")
		os.Exit(1)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Print("retrieval server could not configure database")
		os.Exit(1)
	}
	defer pool.Close()
	records := repository.NewPostgres(pool, repository.Policy{FinalizationLease: configuration.FinalizationLease})
	searcher, err := application.NewSearcher(embedder, store, records, configuration.MinimumSearchScore, configuration.SummaryLLMMaxCalls)
	if err != nil {
		log.Print("retrieval server could not configure search")
		os.Exit(1)
	}
	searcher.SetSummaryProvider(summaryProvider)
	searcher.SetSummaryProviderTimeout(configuration.SummaryLLMTimeout)
	listener, err := net.Listen("tcp", configuration.GRPCAddress)
	if err != nil {
		log.Print("retrieval server listener unavailable")
		os.Exit(1)
	}
	server := grpc.NewServer(
		grpc.Creds(transportCredentials),
		grpc.UnaryInterceptor(grpcauth.UnaryServerInterceptor(grpcauth.Policy{
			Service:  "retrieval.v1.RetrievalService",
			DNSNames: []string{"edge-api", "answer-service"},
		})),
	)
	retrievalv1.RegisterRetrievalServiceServer(server, retrievalgrpc.NewServer(searcher, serviceLogger, configuration.SearchTimeout, configuration.ReadinessProbeTimeout, embedder, store, records))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if configuration.MetricsAddress != "" {
		go serveReadiness(ctx, configuration, embedder, store, records)
	}
	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()
	if err = server.Serve(listener); err != nil && ctx.Err() == nil {
		log.Print("retrieval server stopped because its listener failed")
		os.Exit(1)
	}
}

type readinessDependency interface {
	CheckReady(context.Context) error
}

func serveReadiness(ctx context.Context, configuration config.Config, dependencies ...readinessDependency) {
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		probeContext, cancel := context.WithTimeout(request.Context(), configuration.ReadinessProbeTimeout)
		defer cancel()
		for _, dependency := range dependencies {
			if dependency.CheckReady(probeContext) != nil {
				http.Error(writer, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: configuration.MetricsAddress, Handler: mux, ReadHeaderTimeout: configuration.ReadinessReadHeaderTimeout, IdleTimeout: configuration.ReadinessIdleTimeout}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), configuration.ReadinessShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print("retrieval readiness listener stopped")
	}
}

func readSecret(path string) (string, error) {
	if path == "" {
		return "", os.ErrInvalid
	}
	value, err := os.ReadFile(path) // #nosec G304 -- operator-controlled secret file path.
	if err != nil {
		return "", err
	}
	secret := string(value)
	for len(secret) > 0 && (secret[len(secret)-1] == '\n' || secret[len(secret)-1] == '\r') {
		secret = secret[:len(secret)-1]
	}
	if secret == "" {
		return "", os.ErrInvalid
	}
	return secret, nil
}
