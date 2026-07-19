//go:build integration

package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
	"github.com/belLena81/raglibrarian/services/catalog-service/repository"
)

func TestOutboxPublishesAcceptedUploadAfterBrokerRecovery(t *testing.T) {
	if os.Getenv("CATALOG_OUTBOX_INTEGRATION") != "true" {
		t.Skip("set CATALOG_OUTBOX_INTEGRATION=true inside the Compose test network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dsn := readIntegrationSecret(t, "CATALOG_POSTGRES_DSN_FILE")
	rabbitURI := readIntegrationSecret(t, "CATALOG_RABBITMQ_URI_FILE")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect catalog database: %v", err)
	}
	defer pool.Close()

	id := integrationID(t)
	// Keep the fixture outside the live worker's due window. The test advances
	// its explicit clock to claim and retry the row deterministically.
	now := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	payload := []byte("stable integration payload")
	book := catalog.Book{
		ID:                  "book-" + id,
		Metadata:            catalog.BookMetadata{Title: "Integration", Author: "Contract", Year: 2026, Tags: []string{}},
		ProcessingStatus:    catalog.BookStatusPending,
		ProcessingStage:     catalog.BookStageQueued,
		ProcessingUpdatedAt: now,
		ProcessingVersion:   1,
		CreatedAt:           now,
		ObjectReference:     "originals/book-" + id + ".pdf",
		ByteSize:            5,
		ActorID:             "integration-actor",
	}
	event := catalog.OutboxEvent{
		ID:         "event-" + id,
		Type:       "catalog.book.uploaded.v1",
		OccurredAt: now,
		Payload:    payload,
	}
	store := repository.NewPostgresBookRepository(pool)
	if err = store.Create(ctx, book, event); err != nil {
		t.Fatalf("persist accepted upload: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM catalog.outbox WHERE event_id=$1", event.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM catalog.books WHERE id=$1", book.ID)
	})

	unavailable := NewReconnectingPublisher("amqp://127.0.0.1:1/")
	publishPending(ctx, store, unavailable, &fakeRecorder{}, now)
	_ = unavailable.Close()
	assertOutboxState(t, ctx, pool, event, 1, false)

	recovered := NewReconnectingPublisher(rabbitURI)
	t.Cleanup(func() { _ = recovered.Close() })
	publishPending(ctx, store, recovered, &fakeRecorder{}, now.Add(2*time.Second))
	assertOutboxState(t, ctx, pool, event, 1, true)
}

func readIntegrationSecret(t *testing.T, environmentKey string) string {
	t.Helper()
	path := os.Getenv(environmentKey)
	if path == "" {
		t.Fatalf("%s is required", environmentKey)
	}
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", environmentKey, err)
	}
	secret := strings.TrimSpace(string(value))
	if secret == "" {
		t.Fatalf("%s is empty", environmentKey)
	}
	return secret
}

func integrationID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("generate integration ID: %v", err)
	}
	return hex.EncodeToString(value)
}

func assertOutboxState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, event catalog.OutboxEvent, attempts int, published bool) {
	t.Helper()
	var persistedType string
	var persistedPayload []byte
	var persistedAttempts int
	var publishedAt *time.Time
	err := pool.QueryRow(ctx, `SELECT event_type,payload,attempts,published_at FROM catalog.outbox WHERE event_id=$1`, event.ID).
		Scan(&persistedType, &persistedPayload, &persistedAttempts, &publishedAt)
	if err != nil {
		t.Fatalf("read outbox state: %v", err)
	}
	if persistedType != event.Type || string(persistedPayload) != string(event.Payload) {
		t.Fatal("outbox event changed across broker recovery")
	}
	if persistedAttempts != attempts {
		t.Fatalf("outbox attempts = %d, want %d", persistedAttempts, attempts)
	}
	if (publishedAt != nil) != published {
		t.Fatalf("outbox published = %t, want %t", publishedAt != nil, published)
	}
}
