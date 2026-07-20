//go:build integration

package migrations_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	catalog001Up              = "001_catalog_schema.up.sql"
	catalog001Down            = "001_catalog_schema.down.sql"
	catalog002Up              = "002_catalog_processing_events.up.sql"
	catalog002Down            = "002_catalog_processing_events.down.sql"
	catalog001UpSHA256        = "c6f6abb116ee62d082f86b335c883ca55edb8ce2ddb310cc9fca301196ccc1c1"
	catalog001DownSHA256      = "0b9bf8217c2d7f01cfb330a3daf4797d56f3b9c6000aa2c40311364e213b4dc0"
	catalogMigrationTestLimit = 30 * time.Second
)

func TestCatalogMigrationsRebuildCleanly(t *testing.T) {
	assertCatalog001Checksums(t)
	if os.Getenv("CATALOG_MIGRATION_INTEGRATION") != "true" {
		t.Skip("set CATALOG_MIGRATION_INTEGRATION=true with migration-owner PG environment")
	}
	pool := catalogMigrationPool(t)

	t.Run("base 001 upgrades legacy outbox and enforces final invariants", func(t *testing.T) {
		withCatalogMigrationTransaction(t, pool, func(ctx context.Context, tx pgx.Tx) {
			applyCatalogMigration(t, ctx, tx, catalog001Up)
			fixture := insertCatalogLegacyFixture(t, ctx, tx)
			applyCatalogMigration(t, ctx, tx, catalog002Up)
			assertCatalogLegacyBackfill(t, ctx, tx, fixture)
			assertCatalogProcessingBackfill(t, ctx, tx, fixture.createdAt)
			assertCatalogFinalSchema(t, ctx, tx, fixture)
		})
	})

	t.Run("unmatched legacy outbox fails closed", func(t *testing.T) {
		withCatalogMigrationTransaction(t, pool, func(ctx context.Context, tx pgx.Tx) {
			applyCatalogMigration(t, ctx, tx, catalog001Up)
			createdAt := time.Date(2042, time.January, 2, 3, 4, 5, 6000, time.UTC)
			insertCatalogBook(t, ctx, tx, "unmatched-book", "pending", createdAt)
			insertCatalogLegacyOutbox(t, ctx, tx, legacyOutboxFixture{
				eventID: "unmatched-event", payload: []byte("synthetic unmatched event"), occurredAt: createdAt.Add(time.Second),
			})
			applyCatalogMigrationExpectingFailure(t, ctx, tx, catalog002Up)
			assertCatalogBaseSchema(t, ctx, tx)
		})
	})

	t.Run("ambiguous legacy outbox fails closed", func(t *testing.T) {
		withCatalogMigrationTransaction(t, pool, func(ctx context.Context, tx pgx.Tx) {
			applyCatalogMigration(t, ctx, tx, catalog001Up)
			createdAt := time.Date(2043, time.February, 3, 4, 5, 6, 7000, time.UTC)
			insertCatalogBook(t, ctx, tx, "ambiguous-book-a", "pending", createdAt)
			insertCatalogBook(t, ctx, tx, "ambiguous-book-b", "pending", createdAt)
			insertCatalogLegacyOutbox(t, ctx, tx, legacyOutboxFixture{
				eventID: "ambiguous-event", payload: []byte("synthetic ambiguous event"), occurredAt: createdAt,
			})
			applyCatalogMigrationExpectingFailure(t, ctx, tx, catalog002Up)
			assertCatalogBaseSchema(t, ctx, tx)
		})
	})

	t.Run("002 down and up preserve the legacy upload", func(t *testing.T) {
		withCatalogMigrationTransaction(t, pool, func(ctx context.Context, tx pgx.Tx) {
			applyCatalogMigration(t, ctx, tx, catalog001Up)
			fixture := insertCatalogLegacyFixture(t, ctx, tx)
			applyCatalogMigration(t, ctx, tx, catalog002Up)
			applyCatalogMigration(t, ctx, tx, catalog002Down)
			assertCatalogBaseSchema(t, ctx, tx)
			assertCatalogLegacyEventWithoutAggregate(t, ctx, tx, fixture)
			applyCatalogMigration(t, ctx, tx, catalog002Up)
			assertCatalogLegacyBackfill(t, ctx, tx, fixture)
			assertCatalogFinalSchema(t, ctx, tx, fixture)
		})
	})

	t.Run("001 down and up rebuild an empty base", func(t *testing.T) {
		withCatalogMigrationTransaction(t, pool, func(ctx context.Context, tx pgx.Tx) {
			applyCatalogMigration(t, ctx, tx, catalog001Up)
			applyCatalogMigration(t, ctx, tx, catalog001Down)
			assertCatalogTablesAbsent(t, ctx, tx)
			applyCatalogMigration(t, ctx, tx, catalog001Up)
			assertCatalogBaseSchema(t, ctx, tx)
		})
	})
}

type legacyOutboxFixture struct {
	bookID        string
	eventID       string
	payload       []byte
	createdAt     time.Time
	occurredAt    time.Time
	nextAttemptAt time.Time
	leasedUntil   time.Time
	publishedAt   time.Time
	attempts      int
}

func insertCatalogLegacyFixture(t *testing.T, ctx context.Context, tx pgx.Tx) legacyOutboxFixture {
	t.Helper()
	createdAt := time.Date(2041, time.March, 4, 5, 6, 7, 8000, time.UTC)
	for _, fixture := range []struct {
		id     string
		status string
		offset time.Duration
	}{
		{id: "legacy-pending", status: "pending", offset: 0},
		{id: "legacy-failed", status: "failed", offset: time.Second},
		{id: "legacy-indexed", status: "indexed", offset: 2 * time.Second},
	} {
		insertCatalogBook(t, ctx, tx, fixture.id, fixture.status, createdAt.Add(fixture.offset))
	}
	fixture := legacyOutboxFixture{
		bookID:        "legacy-pending",
		eventID:       "legacy-upload-event",
		payload:       []byte("synthetic bounded legacy upload envelope"),
		createdAt:     createdAt,
		occurredAt:    createdAt,
		nextAttemptAt: createdAt.Add(time.Hour),
		leasedUntil:   createdAt.Add(2 * time.Minute),
		publishedAt:   createdAt.Add(3 * time.Minute),
		attempts:      3,
	}
	insertCatalogLegacyOutbox(t, ctx, tx, fixture)
	return fixture
}

func insertCatalogBook(t *testing.T, ctx context.Context, tx pgx.Tx, id, status string, createdAt time.Time) {
	t.Helper()
	checksum := bytes.Repeat([]byte{0x4a}, sha256.Size)
	_, err := tx.Exec(ctx, `INSERT INTO catalog.books
		(id,title,author,year,tags,processing_status,created_at,object_reference,checksum,byte_size,media_type,actor_id)
		VALUES ($1,'Synthetic migration fixture','RAGLibrarian QA',2026,ARRAY['synthetic'],$2,$3,$4,$5,42,'application/pdf','migration-test')`,
		id, status, createdAt, "originals/"+id+".pdf", checksum)
	if err != nil {
		t.Fatal("catalog migration book fixture could not be inserted")
	}
}

func insertCatalogLegacyOutbox(t *testing.T, ctx context.Context, tx pgx.Tx, fixture legacyOutboxFixture) {
	t.Helper()
	_, err := tx.Exec(ctx, `INSERT INTO catalog.outbox
		(event_id,event_type,payload,occurred_at,attempts,next_attempt_at,leased_until,published_at)
		VALUES ($1,'catalog.book.uploaded.v1',$2,$3,$4,$5,$6,$7)`,
		fixture.eventID, fixture.payload, fixture.occurredAt, fixture.attempts, fixture.nextAttemptAt, fixture.leasedUntil, fixture.publishedAt)
	if err != nil {
		t.Fatal("catalog migration outbox fixture could not be inserted")
	}
}

func assertCatalogLegacyBackfill(t *testing.T, ctx context.Context, tx pgx.Tx, fixture legacyOutboxFixture) {
	t.Helper()
	var eventType, aggregateID string
	var payload []byte
	var sequence int64
	var attempts int
	var occurredAt, nextAttemptAt time.Time
	var leasedUntil, publishedAt *time.Time
	err := tx.QueryRow(ctx, `SELECT event_type,aggregate_id,sequence,payload,occurred_at,attempts,next_attempt_at,leased_until,published_at
		FROM catalog.outbox WHERE event_id=$1`, fixture.eventID).
		Scan(&eventType, &aggregateID, &sequence, &payload, &occurredAt, &attempts, &nextAttemptAt, &leasedUntil, &publishedAt)
	if err != nil {
		t.Fatal("catalog migrated legacy outbox row could not be read")
	}
	if eventType != "catalog.book.uploaded.v1" || aggregateID != fixture.bookID || sequence != 0 ||
		!bytes.Equal(payload, fixture.payload) || !occurredAt.Equal(fixture.occurredAt) || attempts != fixture.attempts ||
		!nextAttemptAt.Equal(fixture.nextAttemptAt) || leasedUntil == nil || !leasedUntil.Equal(fixture.leasedUntil) ||
		publishedAt == nil || !publishedAt.Equal(fixture.publishedAt) {
		t.Fatal("catalog migration did not preserve and backfill the legacy upload event")
	}
	var statusEvents int
	if err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM catalog.outbox WHERE event_type='catalog.book.processing-status-changed.v1'`).Scan(&statusEvents); err != nil {
		t.Fatal("catalog migrated status-event count could not be read")
	}
	if statusEvents != 0 {
		t.Fatal("catalog migration synthesized historical processing status events")
	}
}

func assertCatalogLegacyEventWithoutAggregate(t *testing.T, ctx context.Context, tx pgx.Tx, fixture legacyOutboxFixture) {
	t.Helper()
	var eventType string
	var payload []byte
	var occurredAt time.Time
	var attempts int
	err := tx.QueryRow(ctx, `SELECT event_type,payload,occurred_at,attempts FROM catalog.outbox WHERE event_id=$1`, fixture.eventID).
		Scan(&eventType, &payload, &occurredAt, &attempts)
	if err != nil || eventType != "catalog.book.uploaded.v1" || !bytes.Equal(payload, fixture.payload) ||
		!occurredAt.Equal(fixture.occurredAt) || attempts != fixture.attempts {
		t.Fatal("catalog 002 down migration did not preserve the legacy upload event")
	}
}

func assertCatalogProcessingBackfill(t *testing.T, ctx context.Context, tx pgx.Tx, pendingCreatedAt time.Time) {
	t.Helper()
	expected := map[string]string{
		"legacy-pending": "queued",
		"legacy-failed":  "failed",
		"legacy-indexed": "chunks_ready",
	}
	rows, err := tx.Query(ctx, `SELECT id,processing_stage,processing_failure_category,processing_version,processing_updated_at
		FROM catalog.books ORDER BY id`)
	if err != nil {
		t.Fatal("catalog processing backfill could not be queried")
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id, stage, category string
		var version int64
		var updatedAt time.Time
		if err = rows.Scan(&id, &stage, &category, &version, &updatedAt); err != nil {
			t.Fatal("catalog processing backfill row could not be read")
		}
		if expected[id] != stage || category != "" || version != 1 {
			t.Fatal("catalog processing projection was backfilled incorrectly")
		}
		if id == "legacy-pending" && !updatedAt.Equal(pendingCreatedAt) {
			t.Fatal("catalog processing update timestamp did not preserve creation time")
		}
		seen++
	}
	if rows.Err() != nil || seen != len(expected) {
		t.Fatal("catalog processing backfill returned an unexpected row set")
	}
}

func assertCatalogFinalSchema(t *testing.T, ctx context.Context, tx pgx.Tx, fixture legacyOutboxFixture) {
	t.Helper()
	assertCatalogRegclass(t, ctx, tx, "catalog.processing_inbox", true)
	assertCatalogRegclass(t, ctx, tx, "catalog.outbox_aggregate_sequence_idx", true)
	assertCatalogRegclass(t, ctx, tx, "catalog.outbox_pending_idx", true)
	assertCatalogColumn(t, ctx, tx, "outbox", "aggregate_id", true)
	assertCatalogColumn(t, ctx, tx, "outbox", "sequence", true)
	for _, column := range []string{"processing_stage", "processing_failure_category", "processing_updated_at", "processing_version"} {
		assertCatalogColumn(t, ctx, tx, "books", column, true)
	}

	var aggregateIndex, pendingIndex string
	if err := tx.QueryRow(ctx, `SELECT pg_get_indexdef('catalog.outbox_aggregate_sequence_idx'::regclass)`).Scan(&aggregateIndex); err != nil {
		t.Fatal("catalog aggregate ordering index could not be inspected")
	}
	if err := tx.QueryRow(ctx, `SELECT pg_get_indexdef('catalog.outbox_pending_idx'::regclass)`).Scan(&pendingIndex); err != nil {
		t.Fatal("catalog pending index could not be inspected")
	}
	if !strings.Contains(aggregateIndex, "UNIQUE INDEX") || !strings.Contains(aggregateIndex, "(aggregate_id, sequence)") ||
		!strings.Contains(pendingIndex, "(next_attempt_at, occurred_at, aggregate_id, sequence, event_id)") ||
		!strings.Contains(pendingIndex, "WHERE (published_at IS NULL)") {
		t.Fatal("catalog final outbox indexes do not enforce ordered pending publication")
	}

	assertCatalogStatementRejected(t, ctx, tx, `INSERT INTO catalog.outbox
		(event_id,event_type,aggregate_id,sequence,payload,occurred_at,next_attempt_at)
		VALUES ('duplicate-sequence','catalog.book.uploaded.v1',$1,0,$2,$3,$3)`, fixture.bookID, []byte("synthetic duplicate"), fixture.occurredAt)
	assertCatalogStatementRejected(t, ctx, tx, `UPDATE catalog.outbox SET sequence=-1 WHERE event_id=$1`, fixture.eventID)
	assertCatalogStatementRejected(t, ctx, tx, `UPDATE catalog.books SET processing_version=0 WHERE id=$1`, fixture.bookID)
	assertCatalogStatementRejected(t, ctx, tx, `UPDATE catalog.books SET processing_stage='unknown' WHERE id=$1`, fixture.bookID)
	assertCatalogStatementRejected(t, ctx, tx, `INSERT INTO catalog.processing_inbox
		(event_id,event_type,payload_sha256,processed_at)
		VALUES ('invalid-inbox','ingestion.book.unknown.v1',$1,$2)`, bytes.Repeat([]byte{1}, sha256.Size), fixture.occurredAt)
}

func assertCatalogBaseSchema(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	assertCatalogRegclass(t, ctx, tx, "catalog.books", true)
	assertCatalogRegclass(t, ctx, tx, "catalog.outbox", true)
	assertCatalogRegclass(t, ctx, tx, "catalog.processing_inbox", false)
	assertCatalogRegclass(t, ctx, tx, "catalog.outbox_aggregate_sequence_idx", false)
	assertCatalogColumn(t, ctx, tx, "outbox", "aggregate_id", false)
	assertCatalogColumn(t, ctx, tx, "outbox", "sequence", false)
	assertCatalogColumn(t, ctx, tx, "books", "processing_stage", false)

	var pendingIndex string
	if err := tx.QueryRow(ctx, `SELECT pg_get_indexdef('catalog.outbox_pending_idx'::regclass)`).Scan(&pendingIndex); err != nil {
		t.Fatal("catalog base pending index could not be inspected")
	}
	if !strings.Contains(pendingIndex, "(next_attempt_at, event_id)") || !strings.Contains(pendingIndex, "WHERE (published_at IS NULL)") {
		t.Fatal("catalog base pending index changed incompatibly")
	}
}

func assertCatalogTablesAbsent(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	assertCatalogRegclass(t, ctx, tx, "catalog.books", false)
	assertCatalogRegclass(t, ctx, tx, "catalog.outbox", false)
}

func assertCatalogRegclass(t *testing.T, ctx context.Context, tx pgx.Tx, name string, wantPresent bool) {
	t.Helper()
	var value *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass($1)::text`, name).Scan(&value); err != nil {
		t.Fatal("catalog schema object could not be inspected")
	}
	if (value != nil) != wantPresent {
		t.Fatal("catalog schema object presence did not match the migration contract")
	}
}

func assertCatalogColumn(t *testing.T, ctx context.Context, tx pgx.Tx, table, column string, wantPresent bool) {
	t.Helper()
	var nullable *string
	err := tx.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
		WHERE table_schema='catalog' AND table_name=$1 AND column_name=$2`, table, column).Scan(&nullable)
	if wantPresent {
		if err != nil || nullable == nil || *nullable != "NO" {
			t.Fatal("catalog required non-null column is missing or nullable")
		}
		return
	}
	if err != pgx.ErrNoRows {
		t.Fatal("catalog legacy schema unexpectedly contains a new column")
	}
}

func assertCatalogStatementRejected(t *testing.T, ctx context.Context, tx pgx.Tx, statement string, arguments ...any) {
	t.Helper()
	if _, err := tx.Exec(ctx, "SAVEPOINT expected_catalog_constraint_failure"); err != nil {
		t.Fatal("catalog constraint savepoint could not be created")
	}
	_, rejectedErr := tx.Exec(ctx, statement, arguments...)
	if rejectedErr == nil {
		t.Fatal("catalog schema accepted data that violates a required invariant")
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT expected_catalog_constraint_failure"); err != nil {
		t.Fatal("catalog constraint savepoint could not be restored")
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT expected_catalog_constraint_failure"); err != nil {
		t.Fatal("catalog constraint savepoint could not be released")
	}
}

func applyCatalogMigrationExpectingFailure(t *testing.T, ctx context.Context, tx pgx.Tx, name string) {
	t.Helper()
	if _, err := tx.Exec(ctx, "SAVEPOINT expected_catalog_migration_failure"); err != nil {
		t.Fatal("catalog migration failure savepoint could not be created")
	}
	_, migrationErr := tx.Exec(ctx, readCatalogMigration(t, name))
	if migrationErr == nil {
		t.Fatal("catalog migration accepted legacy data without exactly one book match")
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT expected_catalog_migration_failure"); err != nil {
		t.Fatal("catalog migration failure savepoint could not be restored")
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT expected_catalog_migration_failure"); err != nil {
		t.Fatal("catalog migration failure savepoint could not be released")
	}
}

func applyCatalogMigration(t *testing.T, ctx context.Context, tx pgx.Tx, name string) {
	t.Helper()
	if _, err := tx.Exec(ctx, readCatalogMigration(t, name)); err != nil {
		t.Fatal("catalog migration could not be applied")
	}
}

func withCatalogMigrationTransaction(t *testing.T, pool *pgxpool.Pool, run func(context.Context, pgx.Tx)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), catalogMigrationTestLimit)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal("catalog migration test transaction could not be started")
	}
	defer func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rollbackCancel()
		if err := tx.Rollback(rollbackCtx); err != nil && err != pgx.ErrTxClosed {
			t.Error("catalog migration test transaction could not be rolled back")
		}
	}()
	if _, err = tx.Exec(ctx, `DROP SCHEMA IF EXISTS catalog CASCADE; CREATE SCHEMA catalog`); err != nil {
		t.Fatal("catalog migration test schema could not be isolated")
	}
	run(ctx, tx)
}

func catalogMigrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig("")
	if err != nil {
		t.Fatal("catalog migration-owner PostgreSQL environment is invalid")
	}
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("catalog migration-owner database connection failed")
	}
	t.Cleanup(pool.Close)
	if err = pool.Ping(ctx); err != nil {
		t.Fatal("catalog migration-owner database is unavailable")
	}
	return pool
}

func assertCatalog001Checksums(t *testing.T) {
	t.Helper()
	for name, expected := range map[string]string{
		catalog001Up:   catalog001UpSHA256,
		catalog001Down: catalog001DownSHA256,
	} {
		contents := readCatalogMigrationBytes(t, name)
		digest := sha256.Sum256(contents)
		if hex.EncodeToString(digest[:]) != expected {
			t.Fatal("immutable Catalog migration 001 checksum changed")
		}
	}
}

func readCatalogMigration(t *testing.T, name string) string {
	t.Helper()
	return string(readCatalogMigrationBytes(t, name))
}

func readCatalogMigrationBytes(t *testing.T, name string) []byte {
	t.Helper()
	contents, err := os.ReadFile(name) // #nosec G304 -- callers pass fixed migration fixture names only.
	if err != nil {
		t.Fatal("catalog migration fixture could not be read")
	}
	return contents
}
