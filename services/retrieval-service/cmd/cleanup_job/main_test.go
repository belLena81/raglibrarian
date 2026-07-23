package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/vector"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRunDropsPrivilegesBeforeDatabaseAndVectorInitialization(t *testing.T) {
	previousDrop := dropPrivileges
	previousPool := newPool
	previousQdrant := newQdrant
	t.Cleanup(func() {
		dropPrivileges = previousDrop
		newPool = previousPool
		newQdrant = previousQdrant
	})

	steps := []string{}
	dropPrivileges = func(process.Identity) error {
		steps = append(steps, "drop")
		return nil
	}
	newPool = func(_ context.Context, dsn string) (*pgxpool.Pool, error) {
		if dsn == "" {
			t.Fatal("database DSN is required")
		}
		steps = append(steps, "pool")
		return nil, nil
	}
	newQdrant = func(_, _, _ string, _ *http.Client) (*vector.Qdrant, error) {
		steps = append(steps, "qdrant")
		return nil, nil
	}

	dsnPath := writeCleanupSecret(t, "cleanup-dsn", "postgres://localhost/retrieval")
	qdrantPath := writeCleanupSecret(t, "cleanup-qdrant", "qdrant-key")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", dsnPath)
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", qdrantPath)
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://127.0.0.1:6333")
	t.Setenv("RUN_AS_UID", "123")
	t.Setenv("RUN_AS_GID", "456")

	err := run(context.Background())
	if err == nil {
		t.Fatal("run() expected to fail with stubbed database")
	}
	if len(steps) != 2 || steps[0] != "drop" || steps[1] != "pool" {
		t.Fatalf("run() steps = %#v", steps)
	}
}

func TestRunStopsWhenPrivilegeDropFails(t *testing.T) {
	dropErr := errors.New("permission denied")
	previousDrop := dropPrivileges
	previousPool := newPool
	t.Cleanup(func() {
		dropPrivileges = previousDrop
		newPool = previousPool
	})
	dropPrivileges = func(process.Identity) error {
		return dropErr
	}
	newPool = func(context.Context, string) (*pgxpool.Pool, error) {
		t.Fatal("newPool() should not be called")
		return nil, nil
	}

	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", writeCleanupSecret(t, "cleanup-dsn", "postgres://localhost/retrieval"))
	t.Setenv("RETRIEVAL_QDRANT_API_KEY_FILE", writeCleanupSecret(t, "cleanup-qdrant", "qdrant-key"))
	t.Setenv("RETRIEVAL_QDRANT_URL", "http://127.0.0.1:6333")

	err := run(context.Background())
	if !errors.Is(err, dropErr) {
		t.Fatalf("run() error = %v, want %v", err, dropErr)
	}
}

func writeCleanupSecret(t *testing.T, name, value string) string {
	t.Helper()
	path := t.TempDir() + "/" + name
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("writeCleanupSecret() error = %v", err)
	}
	return path
}
