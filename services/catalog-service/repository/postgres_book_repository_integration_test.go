//go:build integration

package repository

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
)

func TestFinalDeletionAcknowledgementEmitsAtomicStatusProjection(t *testing.T) {
	if os.Getenv("CATALOG_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set CATALOG_POSTGRES_INTEGRATION=true inside the Compose test network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readCatalogIntegrationSecret(t, "CATALOG_POSTGRES_DSN_FILE"))
	if err != nil {
		t.Fatalf("connect catalog database: %v", err)
	}
	t.Cleanup(pool.Close)

	id := randomIntegrationID(t)
	bookID, commandID, ackID := "delete-book-"+id, "delete-command-"+id, "delete-ack-"+id
	now := time.Now().UTC().Truncate(time.Microsecond)
	checksum := sha256.Sum256([]byte("deletion fixture " + id))
	_, err = pool.Exec(ctx, `INSERT INTO catalog.books
		(id,title,author,year,tags,processing_status,created_at,object_reference,checksum,byte_size,media_type,actor_id,
		 processing_stage,processing_failure_category,processing_updated_at,processing_version,lifecycle_version,
		 lifecycle_command_id,original_deleted,artifacts_deleted,index_deleted)
		VALUES ($1,'Deletion fixture','Catalog integration',2026,ARRAY['synthetic'],'deleting',$2,$3,$4,1,
		'application/pdf','integration-test','indexed','',$2,5,2,$5,TRUE,TRUE,FALSE)`,
		bookID, now.Add(-time.Minute), "originals/"+bookID+".pdf", checksum[:], commandID)
	if err != nil {
		t.Fatalf("insert deletion book fixture: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO catalog.lifecycle_commands
		(command_id,book_id,command_type,lifecycle_version,actor_id,correlation_id,accepted_at)
		VALUES ($1,$2,'delete',2,'integration-test',$3,$4)`,
		commandID, bookID, "correlation-"+id, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("insert deletion command fixture: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO catalog.outbox
		(event_id,event_type,aggregate_id,sequence,payload,occurred_at,next_attempt_at,published_at)
		VALUES ($1,'catalog.book.uploaded.v1',$2,0,$3,$4,$4,$4)`,
		"published-upload-"+id, bookID, []byte("sensitive published upload metadata"), now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("insert published upload fixture: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM catalog.outbox WHERE aggregate_id=$1", bookID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM catalog.lifecycle_inbox WHERE event_id=$1", ackID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM catalog.lifecycle_commands WHERE command_id=$1", commandID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM catalog.books WHERE id=$1", bookID)
	})

	repository := NewPostgresBookRepository(pool)
	book, changed, err := repository.ApplyLifecycleAck(ctx, catalog.LifecycleAck{
		EventID: ackID, EventType: "retrieval.book.index-deleted.v1", BookID: bookID,
		CommandID: commandID, LifecycleVersion: 2, PayloadSHA256: sha256.Sum256([]byte("ack " + id)),
	}, now)
	if err != nil || !changed || book.ProcessingStatus != catalog.BookStatusDeleted || book.ProcessingVersion != 6 {
		t.Fatalf("ApplyLifecycleAck() = (%+v, %v, %v)", book, changed, err)
	}
	var payload []byte
	if err = pool.QueryRow(ctx, `SELECT payload FROM catalog.outbox
		WHERE aggregate_id=$1 AND event_type='catalog.book.processing-status-changed.v1'`, bookID).Scan(&payload); err != nil {
		t.Fatalf("read final deletion status: %v", err)
	}
	status := &catalogv1.BookProcessingStatusChangedV1{}
	if err = proto.Unmarshal(payload, status); err != nil {
		t.Fatalf("decode final deletion status: %v", err)
	}
	if status.GetProcessingStatus() != "deleted" || status.GetProcessingVersion() != 6 ||
		status.GetLifecycleVersion() != 2 || status.GetCanReindex() {
		t.Fatalf("final deletion status = %+v", status)
	}
	var retainedProjectionColumns, outboxRows int
	var retainedActor, retainedCorrelation *string
	if err = pool.QueryRow(ctx, `SELECT
		num_nonnulls(title,author,year,tags,object_reference,checksum,byte_size,media_type,actor_id,
			manifest_reference,manifest_sha256,processing_stage,processing_failure_category),
		(SELECT COUNT(*) FROM catalog.outbox WHERE aggregate_id=$1)
		FROM catalog.books WHERE id=$1`, bookID).Scan(&retainedProjectionColumns, &outboxRows); err != nil {
		t.Fatalf("read minimal tombstone: %v", err)
	}
	if retainedProjectionColumns != 0 || outboxRows != 1 {
		t.Fatalf("minimal tombstone retained columns=%d outbox rows=%d", retainedProjectionColumns, outboxRows)
	}
	if err = pool.QueryRow(ctx, `SELECT actor_id,correlation_id FROM catalog.lifecycle_commands
		WHERE command_id=$1`, commandID).Scan(&retainedActor, &retainedCorrelation); err != nil {
		t.Fatalf("read scrubbed lifecycle command: %v", err)
	}
	if retainedActor != nil || retainedCorrelation != nil {
		t.Fatalf("lifecycle command retained actor=%v correlation=%v", retainedActor, retainedCorrelation)
	}
	if duplicate, duplicateChanged, duplicateErr := repository.ApplyLifecycleAck(ctx, catalog.LifecycleAck{
		EventID: ackID, EventType: "retrieval.book.index-deleted.v1", BookID: bookID,
		CommandID: commandID, LifecycleVersion: 2, PayloadSHA256: sha256.Sum256([]byte("ack " + id)),
	}, now.Add(time.Second)); duplicateErr != nil || duplicateChanged || duplicate.ID != "" {
		t.Fatalf("duplicate ApplyLifecycleAck() = (%+v, %v, %v)", duplicate, duplicateChanged, duplicateErr)
	}
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM catalog.outbox WHERE aggregate_id=$1`, bookID).Scan(&outboxRows); err != nil || outboxRows != 1 {
		t.Fatalf("duplicate acknowledgement outbox rows=%d error=%v", outboxRows, err)
	}
}

func TestApplyRetrievalTerminalEventIsAtomicAndIdempotent(t *testing.T) {
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

	id := randomIntegrationID(t)
	bookID := "retrieval-book-" + id
	eventID := "retrieval-event-" + id
	statusEventID := "retrieval-status-" + id
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceChecksum := sha256.Sum256([]byte("synthetic source " + id))
	payloadDigest := sha256.Sum256([]byte("synthetic retrieval terminal envelope " + id))
	_, err = pool.Exec(ctx, `INSERT INTO catalog.books
		(id,title,author,year,tags,processing_status,created_at,object_reference,checksum,byte_size,media_type,actor_id,
		 processing_stage,processing_failure_category,processing_updated_at,processing_version)
		VALUES ($1,'Retrieval terminal fixture','Catalog integration',2026,ARRAY['synthetic'],'processing',$2,$3,$4,1,
		'application/pdf','integration-test','extracting','',$2,2)`,
		bookID, now.Add(-time.Minute), "originals/"+bookID+".pdf", sourceChecksum[:])
	if err != nil {
		t.Fatalf("insert retrieval book fixture: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM catalog.outbox WHERE aggregate_id=$1", bookID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM catalog.processing_inbox WHERE event_id=$1", eventID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM catalog.books WHERE id=$1", bookID)
	})

	event := catalog.ProcessingEvent{
		EventID:       eventID,
		EventType:     "retrieval.book.indexed.v1",
		BookID:        bookID,
		SourceSHA256:  sourceChecksum,
		PayloadSHA256: payloadDigest,
		CorrelationID: "correlation-" + id,
		CausationID:   "job-" + id,
		Fact: catalog.ProcessingFact{
			Kind:       catalog.ProcessingIndexed,
			OccurredAt: now,
		},
	}
	repository := NewPostgresBookRepository(pool)
	book, changed, err := repository.ApplyProcessingEvent(ctx, event, statusEventID, now)
	if err != nil || !changed || book.ProcessingStatus != catalog.BookStatusIndexed ||
		book.ProcessingStage != catalog.BookStageIndexed || book.ProcessingVersion != 3 {
		t.Fatalf("ApplyProcessingEvent() = (%+v, %v, %v)", book, changed, err)
	}

	_, changed, err = repository.ApplyProcessingEvent(ctx, event, "unused-status-id", now.Add(time.Second))
	if err != nil || changed {
		t.Fatalf("duplicate ApplyProcessingEvent() = (%v, %v)", changed, err)
	}
	var inboxCount, outboxCount int
	if err = pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM catalog.processing_inbox WHERE event_id=$1),
		(SELECT COUNT(*) FROM catalog.outbox WHERE aggregate_id=$2 AND sequence=3)`, eventID, bookID).
		Scan(&inboxCount, &outboxCount); err != nil {
		t.Fatalf("read terminal projection counts: %v", err)
	}
	if inboxCount != 1 || outboxCount != 1 {
		t.Fatalf("terminal projection counts = inbox %d, outbox %d", inboxCount, outboxCount)
	}

	event.PayloadSHA256 = sha256.Sum256([]byte("conflicting envelope"))
	if _, _, err = repository.ApplyProcessingEvent(ctx, event, "conflict-status-id", now.Add(2*time.Second)); err != catalog.ErrProcessingEventConflict {
		t.Fatalf("conflicting ApplyProcessingEvent() error = %v", err)
	}
}

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
		(event_id,event_type,aggregate_id,sequence,payload,occurred_at,next_attempt_at)
		VALUES ($1,'catalog.book.uploaded.v1',$1,0,$2,$3,$4)`,
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

func TestClaimOutboxDoesNotOvertakeAggregateSequence(t *testing.T) {
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

	aggregateID := "ordering-" + randomIntegrationID(t)
	eventIDs := []string{aggregateID + "-uploaded", aggregateID + "-queued"}
	now := time.Now().UTC().Truncate(time.Microsecond)
	for sequence, eventID := range eventIDs {
		_, err = pool.Exec(ctx, `INSERT INTO catalog.outbox
			(event_id,event_type,aggregate_id,sequence,payload,occurred_at,next_attempt_at)
			VALUES ($1,'catalog.book.uploaded.v1',$2,$3,$4,$5,$5)`,
			eventID, aggregateID, sequence, []byte("ordering fixture"), now)
		if err != nil {
			t.Fatalf("insert sequence %d: %v", sequence, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM catalog.outbox WHERE aggregate_id=$1", aggregateID)
	})

	repository := NewPostgresBookRepository(pool)
	claimed, err := repository.ClaimOutbox(ctx, now, 30*time.Second)
	if err != nil {
		t.Fatalf("claim upload: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != eventIDs[0] {
		t.Fatalf("first claim = %#v, want only %q", claimed, eventIDs[0])
	}
	if err = repository.MarkPublished(ctx, eventIDs[0], now); err != nil {
		t.Fatalf("mark upload published: %v", err)
	}
	claimed, err = repository.ClaimOutbox(ctx, now, 30*time.Second)
	if err != nil {
		t.Fatalf("claim queued status: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != eventIDs[1] {
		t.Fatalf("second claim = %#v, want only %q", claimed, eventIDs[1])
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
