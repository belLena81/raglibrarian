package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"
)

func TestRunDropsPrivilegesBeforeRuntimeDependencies(t *testing.T) {
	previousDrop := dropPrivileges
	previousPool := newPool
	previousDial := dialPublisher
	t.Cleanup(func() {
		dropPrivileges = previousDrop
		newPool = previousPool
		dialPublisher = previousDial
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
	dialPublisher = func(_ string) (*amqp091.Connection, error) {
		steps = append(steps, "dial")
		return nil, nil
	}

	dsnPath := writeSecret(t, "dispatcher-dsn", "postgres://localhost/retrieval")
	uriPath := writeSecret(t, "dispatcher-uri", "amqps://guest:guest@rabbitmq:5671")
	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", dsnPath)
	t.Setenv("RETRIEVAL_RABBITMQ_PUBLISHER_URI_FILE", uriPath)
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

	t.Setenv("RETRIEVAL_POSTGRES_DSN_FILE", writeSecret(t, "dispatcher-dsn-priv", "postgres://localhost/retrieval"))
	t.Setenv("RETRIEVAL_RABBITMQ_PUBLISHER_URI_FILE", writeSecret(t, "dispatcher-uri-priv", "amqps://guest:guest@rabbitmq:5671"))

	err := run(context.Background())
	if !errors.Is(err, dropErr) {
		t.Fatalf("run() error = %v, want %v", err, dropErr)
	}
}

func writeSecret(t *testing.T, name, value string) string {
	t.Helper()
	path := t.TempDir() + "/" + name
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("writeSecret() error = %v", err)
	}
	return path
}
