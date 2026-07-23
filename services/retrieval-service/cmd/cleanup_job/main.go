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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/repository"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/vector"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultRunAs = 65532

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
	runAs, err := parseRunAs()
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
	records := repository.NewPostgres(pool)
	index, err := newQdrant(qdrantURL, "evidence_v2", qdrantKey, &http.Client{Timeout: 90 * time.Second})
	if err != nil {
		return err
	}
	if index == nil {
		return errors.New("invalid vector client")
	}
	now := time.Now().UTC()
	jobs, err := records.PendingVectorCleanup(ctx, 64, now)
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
	return lifecycle.RetryDeletions(ctx, 64)
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

func parseRunAs() (process.Identity, error) {
	uid, err := parseRunAsIdentity("RUN_AS_UID")
	if err != nil {
		return process.Identity{}, err
	}
	gid, err := parseRunAsIdentity("RUN_AS_GID")
	if err != nil {
		return process.Identity{}, err
	}
	return process.Identity{UID: uid, GID: gid}, nil
}

func parseRunAsIdentity(name string) (int, error) {
	value := os.Getenv(name)
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultRunAs, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, errors.New("invalid process identity")
	}
	return parsed, nil
}
