package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/belLena81/raglibrarian/pkg/process"
	retrievalconfig "github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/repository"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/vector"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	dropPrivileges = process.DropPrivileges
	newPool        = pgxpool.New
	newQdrant      = vector.NewAuthenticatedQdrant
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	dsn, err := readSecretFile("RETRIEVAL_POSTGRES_DSN_FILE", 4096)
	if err != nil {
		return err
	}
	qdrantKey, err := readSecretFile("RETRIEVAL_QDRANT_API_KEY_FILE", 1024)
	if err != nil {
		return err
	}
	qdrantURL := os.Getenv("RETRIEVAL_QDRANT_URL")
	if !privateURL(qdrantURL) {
		return errors.New("invalid private vector endpoint")
	}
	runAs, err := retrievalconfig.LoadRunAs()
	if err != nil {
		return err
	}
	if err = dropPrivileges(runAs); err != nil {
		return err
	}
	pool, err := newPool(ctx, dsn)
	if err != nil {
		return errors.New("database unavailable")
	}
	if pool == nil {
		return errors.New("database unavailable")
	}
	defer pool.Close()
	policy, err := retrievalconfig.LoadLambdaRuntimePolicy()
	if err != nil {
		return err
	}
	cleanupPolicy, err := retrievalconfig.LoadCleanupJobPolicy()
	if err != nil {
		return err
	}
	records := repository.NewPostgres(pool, repository.Policy{FinalizationLease: policy.FinalizationLease})
	minimumSearchScore, err := retrievalconfig.LoadMinimumSearchScore()
	if err != nil {
		return err
	}
	index, err := newQdrant(qdrantURL, retrievalconfig.DefaultQdrantCollection, qdrantKey, &http.Client{Timeout: cleanupPolicy.DependencyTimeout}, minimumSearchScore)
	if err != nil {
		return err
	}
	if index == nil {
		return errors.New("invalid vector client")
	}
	now := time.Now().UTC()
	jobs, err := records.PendingVectorCleanup(ctx, cleanupPolicy.BatchSize, now)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if err = index.DeleteJob(ctx, job.JobID); err != nil {
			if retryErr := records.RetryVectorCleanup(ctx, job.JobID, now); retryErr != nil {
				return retryErr
			}
			continue
		}
		if err = records.CompleteVectorCleanup(ctx, job.JobID); err != nil {
			return err
		}
	}
	lifecycle, err := application.NewLifecycleCoordinator(records, index, randomID, time.Now)
	if err != nil {
		return err
	}
	return lifecycle.RetryDeletions(ctx, cleanupPolicy.BatchSize)
}

func readSecretFile(key string, maximum int64) (string, error) {
	path := os.Getenv(key)
	if path == "" {
		return "", errors.New("missing secret file")
	}
	file, err := process.OpenSecretFile(path, maximum)
	if err != nil {
		return "", errors.New("invalid secret file")
	}
	defer func() { _ = file.Close() }()
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return "", errors.New("read secret file")
	}
	return strings.TrimSpace(string(value)), nil
}

func privateURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host := parsed.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || os.Getenv("RETRIEVAL_SERVERLESS_PRIVATE_HOST") == host
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
