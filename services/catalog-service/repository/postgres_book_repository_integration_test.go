//go:build integration

package repository

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestListPaginatesBooksWithSharedTimestamp(t *testing.T) {
	if os.Getenv("CATALOG_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set CATALOG_POSTGRES_INTEGRATION=true inside the Compose test network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dsn := readCatalogIntegrationSecret(t, "CATALOG_POSTGRES_DSN_FILE")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect catalog database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := randomIntegrationID(t)
	bookIDs := []string{prefix + "-a", prefix + "-b", prefix + "-c"}
	createdAt := time.Date(2100, time.January, 1, 0, 0, 0, 123456000, time.UTC)
	for _, bookID := range bookIDs {
		_, err = pool.Exec(ctx, `INSERT INTO catalog.books
            (id,title,author,year,tags,processing_status,created_at,object_reference,checksum,byte_size,media_type,actor_id)
            VALUES ($1,'Pagination fixture','Catalog integration',2026,ARRAY['pagination'],'indexed',$2,$3,$4,1,'application/pdf','integration-test')`,
			bookID, createdAt, "books/"+bookID+".pdf", bytes.Repeat([]byte{1}, 32))
		if err != nil {
			t.Fatalf("insert book fixture %q: %v", bookID, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, "DELETE FROM catalog.books WHERE id=ANY($1)", bookIDs); cleanupErr != nil {
			t.Errorf("delete book fixtures: %v", cleanupErr)
		}
	})

	repository := NewPostgresBookRepository(pool)
	firstPage, nextPageToken, err := repository.List(ctx, 2, "")
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(firstPage) != 2 || firstPage[0].ID != bookIDs[2] || firstPage[1].ID != bookIDs[1] {
		t.Fatalf("first page = %#v, want IDs [%s %s]", firstPage, bookIDs[2], bookIDs[1])
	}
	if nextPageToken == "" {
		t.Fatal("first page token is empty")
	}

	secondPage, finalPageToken, err := repository.List(ctx, 2, nextPageToken)
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(secondPage) != 1 || secondPage[0].ID != bookIDs[0] {
		t.Fatalf("second page = %#v, want ID [%s]", secondPage, bookIDs[0])
	}
	if finalPageToken != "" {
		t.Fatalf("final page token = %q, want empty", finalPageToken)
	}
}

func TestOutboxBacklogScansFractionalOldestAge(t *testing.T) {
	if os.Getenv("CATALOG_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set CATALOG_POSTGRES_INTEGRATION=true inside the Compose test network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dsn := readCatalogIntegrationSecret(t, "CATALOG_POSTGRES_DSN_FILE")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect catalog database: %v", err)
	}
	t.Cleanup(pool.Close)

	eventID := "backlog-" + randomIntegrationID(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	repository := NewPostgresBookRepository(pool)
	baseline, err := repository.OutboxBacklog(ctx, now)
	if err != nil {
		t.Fatalf("read outbox backlog baseline: %v", err)
	}
	fixtureAge := time.Duration(baseline.OldestAgeSecond+1)*time.Second + 750*time.Millisecond
	_, err = pool.Exec(ctx, `INSERT INTO catalog.outbox
        (event_id,event_type,payload,occurred_at,next_attempt_at)
        VALUES ($1,'catalog.book.uploaded.v1',$2,$3,$4)`,
		eventID, []byte("backlog integration fixture"), now.Add(-fixtureAge), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("insert outbox fixture: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, "DELETE FROM catalog.outbox WHERE event_id=$1", eventID); cleanupErr != nil {
			t.Errorf("delete outbox fixture: %v", cleanupErr)
		}
	})

	backlog, err := repository.OutboxBacklog(ctx, now)
	if err != nil {
		t.Fatalf("read fractional outbox backlog: %v", err)
	}
	if backlog.Pending != baseline.Pending+1 {
		t.Fatalf("pending = %d, want %d", backlog.Pending, baseline.Pending+1)
	}
	if backlog.OldestAgeSecond != baseline.OldestAgeSecond+1 {
		t.Fatalf("oldest age seconds = %d, want %d", backlog.OldestAgeSecond, baseline.OldestAgeSecond+1)
	}

	if err = repository.MarkPublished(ctx, eventID, now); err != nil {
		t.Fatalf("mark outbox fixture published: %v", err)
	}
	backlog, err = repository.OutboxBacklog(ctx, now)
	if err != nil {
		t.Fatalf("read empty outbox backlog: %v", err)
	}
	if backlog != baseline {
		t.Fatalf("published backlog = %+v, want baseline %+v", backlog, baseline)
	}
}

func readCatalogIntegrationSecret(t *testing.T, environmentKey string) string {
	t.Helper()
	path := os.Getenv(environmentKey)
	if path == "" {
		t.Fatalf("%s is required", environmentKey)
	}
	value, err := os.ReadFile(path) // #nosec G304 -- test-only configured Compose secret path.
	if err != nil {
		t.Fatalf("read %s: %v", environmentKey, err)
	}
	secret := strings.TrimSpace(string(value))
	if secret == "" {
		t.Fatalf("%s is empty", environmentKey)
	}
	return secret
}

func randomIntegrationID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("generate integration ID: %v", err)
	}
	return hex.EncodeToString(value)
}
