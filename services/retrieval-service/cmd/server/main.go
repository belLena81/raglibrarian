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
	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/pkg/process"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/pkg/providerhttp"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	retrievalgrpc "github.com/belLena81/raglibrarian/services/retrieval-service/internal/grpc"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/repository"
	retrievalruntime "github.com/belLena81/raglibrarian/services/retrieval-service/internal/runtime"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
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
	httpClient := retrievalruntime.NewDependencyHTTPClient(configuration.DependencyTimeout)
	embedder, err := retrievalruntime.NewEmbedder(configuration, httpClient, serviceLogger)
	if err != nil {
		log.Print("retrieval server could not configure embedding dependency")
		os.Exit(1)
	}
	store, err := retrievalruntime.NewVectorStore(configuration, httpClient)
	if err != nil {
		log.Print("retrieval server could not configure vector dependency")
		os.Exit(1)
	}
	dsn, err := readSecret(configuration.PostgresDSNFile)
	if err != nil {
		log.Print("retrieval server could not read database credentials")
		os.Exit(1)
	}
	var summaryCacheHMACKey []byte
	if configuration.EvidenceAssessor.CacheTTL > 0 {
		summaryCacheHMACKey, err = readSummaryCacheHMACKey(configuration.EvidenceAssessor.CacheHMACKeyFile)
		if err != nil {
			log.Print("retrieval server could not read summary cache HMAC key")
			os.Exit(1)
		}
	}
	evidenceAssessor, err := retrievalruntime.NewEvidenceAssessor(configuration.EvidenceAssessor, serviceLogger)
	if err != nil {
		log.Print("retrieval server could not configure evidence assessor")
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
	searcherOptions := []application.SearcherOption{
		application.WithAssessmentCacheObserver(summaryCacheLogger{log: serviceLogger}),
	}
	if configuration.EvidenceAssessor.CacheTTL > 0 {
		searcherOptions = append(searcherOptions, application.WithAssessmentCache(records))
	}
	searcher, err := application.NewSearcherWithPolicyAndLexical(embedder, store, records, records, evidenceAssessor, application.SearchPolicy{
		MinimumVisibleScore: configuration.MinimumSearchScore,
		AssessmentCallLimit: configuration.EvidenceAssessor.MaxCalls,
		AssessmentTimeout:   configuration.EvidenceAssessor.Timeout,
		AssessmentCache: application.AssessmentCachePolicy{
			TTL:                    configuration.EvidenceAssessor.CacheTTL,
			NegativeReuse:          configuration.EvidenceAssessor.CacheNegativeReuse,
			NegativeMinimumCosine:  configuration.EvidenceAssessor.CacheNegativeMinimumCosine,
			NegativeCandidateLimit: configuration.EvidenceAssessor.CacheNegativeCandidateLimit,
			MaximumEntries:         configuration.EvidenceAssessor.CacheMaxEntries,
			MaximumInputRunes:      configuration.EvidenceAssessor.MaxInputRunes,
			HMACKey:                summaryCacheHMACKey,
			ProviderProfile: application.AssessmentCacheProfile(
				configuration.EvidenceAssessor.BaseURL,
				configuration.EvidenceAssessor.Model,
				configuration.EvidenceAssessor.OutputMode,
				configuration.EvidenceAssessor.MaxOutputTokens,
				configuration.EvidenceAssessor.MaxInputRunes,
				configuration.EvidenceAssessor.MaxResponseBytes,
				configuration.EvidenceAssessor.MaxSummaryBytes,
			),
		},
		CandidatePageMultiplier:     configuration.SearchCandidatePageMultiplier,
		ReciprocalRankFusionK:       configuration.ReciprocalRankFusionK,
		MaximumAssessmentInputRunes: configuration.EvidenceAssessor.MaxInputRunes,
		RequestPolicy: domain.SearchRequestPolicy{
			MaximumQuestionCharacters: configuration.SearchRequestPolicy.MaximumQuestionCharacters,
			MaximumFilterTags:         configuration.SearchRequestPolicy.MaximumFilterTags,
			MaximumTagCharacters:      configuration.SearchRequestPolicy.MaximumTagCharacters,
			MaximumAuthorCharacters:   configuration.SearchRequestPolicy.MaximumAuthorCharacters,
			DefaultResultLimit:        configuration.SearchRequestPolicy.DefaultResultLimit,
			MaximumResultLimit:        configuration.SearchRequestPolicy.MaximumResultLimit,
		},
	}, searcherOptions...)
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
	go serveSummaryCacheCleanup(
		ctx,
		serviceLogger,
		records,
		configuration.SummaryCacheCleanupInterval,
		configuration.SummaryCacheCleanupTimeout,
		configuration.SummaryCacheCleanupBatchSize,
	)
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

type summaryCacheCleanupStore interface {
	DeleteExpiredAssessmentCache(context.Context, int) (int, error)
}

func serveSummaryCacheCleanup(ctx context.Context, log *zap.Logger, store summaryCacheCleanupStore, interval, timeout time.Duration, batchSize int) {
	run := func() {
		cleanupContext, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		deleted, err := cleanupExpiredAssessmentCache(cleanupContext, store, batchSize)
		if log == nil {
			return
		}
		if err != nil {
			log.Info("retrieval.summary.cache.cleanup", zap.String("outcome", "dependency_unavailable"), zap.Int("result_count", deleted))
			return
		}
		log.Info("retrieval.summary.cache.cleanup", zap.String("outcome", "success"), zap.Int("result_count", deleted))
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func cleanupExpiredAssessmentCache(ctx context.Context, store summaryCacheCleanupStore, batchSize int) (int, error) {
	total := 0
	for {
		deleted, err := store.DeleteExpiredAssessmentCache(ctx, batchSize)
		total += deleted
		if err != nil {
			return total, err
		}
		if deleted < batchSize {
			return total, nil
		}
	}
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

func readSummaryCacheHMACKey(path string) ([]byte, error) {
	value, err := providerhttp.ReadSingleLineSecret(path, 4096)
	if err != nil || len(value) < 32 {
		return nil, os.ErrInvalid
	}
	return []byte(value), nil
}

type summaryCacheLogger struct {
	log *zap.Logger
}

func (l summaryCacheLogger) AssessmentCacheSearch(stats application.AssessmentCacheStats) {
	if l.log != nil {
		l.log.Info(
			"retrieval.summary.cache.search",
			zap.Int("cache_hits", stats.Hits),
			zap.Int("cache_negative_hits", stats.NegativeHits),
			zap.Int("cache_misses", stats.Misses),
			zap.Int("cache_semantic_mismatches", stats.SemanticMismatches),
			zap.Int("cache_guard_mismatches", stats.GuardMismatches),
			zap.Int("cache_lookup_errors", stats.LookupErrors),
			zap.Int("cache_stores", stats.Stores),
			zap.Int("cache_store_errors", stats.StoreErrors),
			zap.Int("provider_calls", stats.ProviderCalls),
			zap.Int("local_fallbacks", stats.LocalFallbacks),
		)
	}
}
