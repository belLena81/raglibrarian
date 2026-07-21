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
	"time"

	"github.com/belLena81/raglibrarian/pkg/grpcauth"
	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/embedding"
	retrievalgrpc "github.com/belLena81/raglibrarian/services/retrieval-service/internal/grpc"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/repository"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/vector"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
)

func main() {
	configuration, err := config.Load()
	if err != nil {
		log.Print("retrieval server could not start because configuration was invalid")
		os.Exit(1)
	}
	transportCredentials, err := internaltls.ServerCredentials(configuration.TLS)
	if err != nil {
		log.Print("retrieval server could not load transport credentials")
		os.Exit(1)
	}
	httpClient := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	embedder, err := embedding.NewTEI(configuration.TEIURL, httpClient)
	if err != nil {
		log.Print("retrieval server could not configure embedding dependency")
		os.Exit(1)
	}
	apiKey, err := readSecret(configuration.QdrantAPIKeyFile)
	if err != nil {
		log.Print("retrieval server could not read vector dependency credentials")
		os.Exit(1)
	}
	store, err := vector.NewAuthenticatedQdrant(configuration.QdrantURL, configuration.QdrantCollection, apiKey, httpClient)
	if err != nil {
		log.Print("retrieval server could not configure vector dependency")
		os.Exit(1)
	}
	dsn, err := readSecret(configuration.PostgresDSNFile)
	if err != nil {
		log.Print("retrieval server could not read database credentials")
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
	records := repository.NewPostgres(pool)
	searcher, err := application.NewSearcher(embedder, store, records)
	if err != nil {
		log.Print("retrieval server could not configure search")
		os.Exit(1)
	}
	listener, err := net.Listen("tcp", configuration.GRPCAddress)
	if err != nil {
		log.Print("retrieval server listener unavailable")
		os.Exit(1)
	}
	server := grpc.NewServer(
		grpc.Creds(transportCredentials),
		grpc.UnaryInterceptor(grpcauth.UnaryServerInterceptor(grpcauth.Policy{Service: "retrieval.v1.RetrievalService", DNSName: "edge-api"})),
	)
	retrievalv1.RegisterRetrievalServiceServer(server, retrievalgrpc.NewServer(searcher, embedder, store, records))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if configuration.MetricsAddress != "" {
		go serveReadiness(ctx, configuration.MetricsAddress, embedder, store, records)
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

func serveReadiness(ctx context.Context, address string, dependencies ...readinessDependency) {
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		probeContext, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		for _, dependency := range dependencies {
			if dependency.CheckReady(probeContext) != nil {
				http.Error(writer, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
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
