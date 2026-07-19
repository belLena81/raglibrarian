// Package repository contains Catalog's outward persistence adapters.
package repository

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
)

// PostgresBookRepository persists a book and its outbox record in one transaction.
type PostgresBookRepository struct {
	pool       *pgxpool.Pool
	wakeOutbox func()
}

// PendingOutboxEvent is a leased event awaiting broker confirmation.
type PendingOutboxEvent struct {
	ID       string
	Type     string
	Payload  []byte
	Attempts int
}

// OutboxBacklog is the safe aggregate state used for fixed-label metrics.
type OutboxBacklog struct {
	Pending         int64
	OldestAgeSecond int64
}

func NewPostgresBookRepository(pool *pgxpool.Pool, wakeOutbox ...func()) *PostgresBookRepository {
	if pool == nil {
		panic("repository: pgx pool is required")
	}
	wake := func() {}
	if len(wakeOutbox) > 0 && wakeOutbox[0] != nil {
		wake = wakeOutbox[0]
	}
	return &PostgresBookRepository{pool: pool, wakeOutbox: wake}
}

func (r *PostgresBookRepository) Create(ctx context.Context, book catalog.Book, events ...catalog.OutboxEvent) error {
	if len(events) == 0 {
		return errors.New("catalog: at least one outbox event is required")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("catalog: begin create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `INSERT INTO catalog.books
		(id,title,author,year,tags,processing_status,created_at,object_reference,checksum,byte_size,media_type,actor_id,
		 processing_stage,processing_failure_category,processing_updated_at,processing_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'application/pdf',$11,$12,$13,$14,$15)`,
		book.ID, book.Metadata.Title, book.Metadata.Author, book.Metadata.Year, book.Metadata.Tags,
		string(book.ProcessingStatus), book.CreatedAt, book.ObjectReference, book.Checksum[:], book.ByteSize, book.ActorID,
		string(book.ProcessingStage), string(book.ProcessingFailureCategory), book.ProcessingUpdatedAt, book.ProcessingVersion)
	if err != nil {
		return fmt.Errorf("catalog: insert book: %w", err)
	}
	for _, event := range events {
		_, err = tx.Exec(ctx, `INSERT INTO catalog.outbox
			(event_id,event_type,aggregate_id,sequence,payload,occurred_at,next_attempt_at)
			VALUES ($1,$2,$3,$4,$5,$6,$6)`, event.ID, event.Type, event.AggregateID, event.Sequence, event.Payload, event.OccurredAt)
		if err != nil {
			return fmt.Errorf("catalog: insert outbox: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("catalog: commit create: %w", err)
	}
	r.wakeOutbox()
	return nil
}

func (r *PostgresBookRepository) List(ctx context.Context, size int, token string) ([]catalog.Book, string, error) {
	args := []any{}
	query := `SELECT id,title,author,year,tags,processing_status,created_at,object_reference,checksum,byte_size,
		processing_stage,processing_failure_category,processing_updated_at,processing_version
        FROM catalog.books WHERE processing_status <> 'deleted'`
	if token != "" {
		cursor, err := decodeCursor(token)
		if err != nil {
			return nil, "", catalog.ErrInvalidMetadata
		}
		query += ` AND (created_at,id) < ($1,$2)`
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC,id DESC LIMIT $%d", len(args)+1)
	args = append(args, size+1)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("catalog: list books: %w", err)
	}
	defer rows.Close()
	books := make([]catalog.Book, 0, size+1)
	for rows.Next() {
		book, scanErr := scanBook(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		books = append(books, book)
	}
	if err = rows.Err(); err != nil {
		return nil, "", fmt.Errorf("catalog: list rows: %w", err)
	}
	next := ""
	if len(books) > size {
		last := books[size-1]
		books = books[:size]
		next = encodeCursor(last)
	}
	return books, next, nil
}

func (r *PostgresBookRepository) Get(ctx context.Context, id string) (catalog.Book, error) {
	book, err := scanBook(r.pool.QueryRow(ctx, `SELECT id,title,author,year,tags,processing_status,created_at,object_reference,checksum,byte_size,
		processing_stage,processing_failure_category,processing_updated_at,processing_version
        FROM catalog.books WHERE id=$1 AND processing_status <> 'deleted'`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Book{}, catalog.ErrNotFound
	}
	if err != nil {
		return catalog.Book{}, fmt.Errorf("catalog: get book: %w", err)
	}
	return book, nil
}

// ApplyProcessingEvent deduplicates an Ingestion fact, updates the Book
// aggregate, and creates Catalog's sanitized notification in one transaction.
func (r *PostgresBookRepository) ApplyProcessingEvent(ctx context.Context, event catalog.ProcessingEvent, statusEventID string, appliedAt time.Time) (catalog.Book, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: begin processing event: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `INSERT INTO catalog.processing_inbox
		(event_id,event_type,payload_sha256,processed_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (event_id) DO NOTHING`, event.EventID, event.EventType, event.PayloadSHA256[:], appliedAt)
	if err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: insert processing inbox: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var existingType string
		var existingDigest []byte
		if err = tx.QueryRow(ctx, `SELECT event_type,payload_sha256 FROM catalog.processing_inbox WHERE event_id=$1`, event.EventID).Scan(&existingType, &existingDigest); err != nil {
			return catalog.Book{}, false, fmt.Errorf("catalog: read processing inbox: %w", err)
		}
		if existingType != event.EventType || !bytes.Equal(existingDigest, event.PayloadSHA256[:]) {
			return catalog.Book{}, false, catalog.ErrProcessingEventConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return catalog.Book{}, false, fmt.Errorf("catalog: commit duplicate processing event: %w", err)
		}
		return catalog.Book{}, false, nil
	}

	book, err := scanBook(tx.QueryRow(ctx, `SELECT id,title,author,year,tags,processing_status,created_at,object_reference,checksum,byte_size,
		processing_stage,processing_failure_category,processing_updated_at,processing_version
		FROM catalog.books WHERE id=$1 AND processing_status <> 'deleted' FOR UPDATE`, event.BookID))
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Book{}, false, catalog.ErrNotFound
	}
	if err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: lock processing book: %w", err)
	}
	if !bytes.Equal(book.Checksum[:], event.SourceSHA256[:]) {
		return catalog.Book{}, false, catalog.ErrProcessingEventConflict
	}
	changed, err := book.ApplyProcessingFact(event.Fact)
	if err != nil {
		return catalog.Book{}, false, err
	}
	if changed {
		payload, marshalErr := proto.Marshal(&catalogv1.BookProcessingStatusChangedV1{
			EventId: statusEventID, BookId: book.ID, ProcessingStatus: string(book.ProcessingStatus),
			ProcessingStage: string(book.ProcessingStage), ProcessingFailureCategory: string(book.ProcessingFailureCategory),
			ProcessingVersion: book.ProcessingVersion, UpdatedAt: timestamppb.New(book.ProcessingUpdatedAt),
			CorrelationId: event.CorrelationID, OccurredAt: timestamppb.New(appliedAt), CausationId: event.EventID,
			Producer: "catalog-service", SchemaVersion: "v1",
			IdempotencyKey: fmt.Sprintf("%s:processing:%d", book.ID, book.ProcessingVersion),
		})
		if marshalErr != nil {
			return catalog.Book{}, false, errors.New("catalog: status event unavailable")
		}
		if _, err = tx.Exec(ctx, `UPDATE catalog.books SET processing_status=$2,processing_stage=$3,
			processing_failure_category=$4,processing_updated_at=$5,processing_version=$6 WHERE id=$1`,
			book.ID, string(book.ProcessingStatus), string(book.ProcessingStage), string(book.ProcessingFailureCategory),
			book.ProcessingUpdatedAt, book.ProcessingVersion); err != nil {
			return catalog.Book{}, false, fmt.Errorf("catalog: update processing book: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO catalog.outbox
			(event_id,event_type,aggregate_id,sequence,payload,occurred_at,next_attempt_at)
			VALUES ($1,'catalog.book.processing-status-changed.v1',$2,$3,$4,$5,$5)`, statusEventID, book.ID, book.ProcessingVersion, payload, appliedAt); err != nil {
			return catalog.Book{}, false, fmt.Errorf("catalog: insert processing status outbox: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: commit processing event: %w", err)
	}
	if changed {
		r.wakeOutbox()
	}
	return book, changed, nil
}

func (r *PostgresBookRepository) ReferencesExist(ctx context.Context, references []string) (map[string]bool, error) {
	result := make(map[string]bool, len(references))
	for _, reference := range references {
		result[reference] = false
	}
	if len(references) == 0 {
		return result, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT object_reference FROM catalog.books WHERE object_reference = ANY($1)`, references)
	if err != nil {
		return nil, fmt.Errorf("catalog: lookup object references: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var reference string
		if err = rows.Scan(&reference); err != nil {
			return nil, fmt.Errorf("catalog: scan object reference: %w", err)
		}
		result[reference] = true
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: object reference rows: %w", err)
	}
	return result, nil
}

func (r *PostgresBookRepository) OutboxBacklog(ctx context.Context, now time.Time) (OutboxBacklog, error) {
	var backlog OutboxBacklog
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*), COALESCE(FLOOR(EXTRACT(EPOCH FROM ($1 - MIN(occurred_at))))::bigint, 0::bigint)
        FROM catalog.outbox WHERE published_at IS NULL`, now).Scan(&backlog.Pending, &backlog.OldestAgeSecond)
	if err != nil {
		return OutboxBacklog{}, fmt.Errorf("catalog: outbox backlog: %w", err)
	}
	return backlog, nil
}

// ClaimOutbox leases a bounded batch while preventing a later event for an
// aggregate from overtaking an earlier unpublished event.
func (r *PostgresBookRepository) ClaimOutbox(ctx context.Context, now time.Time, lease time.Duration) ([]PendingOutboxEvent, error) {
	rows, err := r.pool.Query(ctx, `WITH candidates AS (
		SELECT candidate.event_id FROM catalog.outbox AS candidate
		WHERE candidate.published_at IS NULL AND candidate.next_attempt_at <= $1
		  AND (candidate.leased_until IS NULL OR candidate.leased_until <= $1)
		  AND NOT EXISTS (
			SELECT 1 FROM catalog.outbox AS predecessor
			WHERE predecessor.aggregate_id=candidate.aggregate_id
			  AND predecessor.published_at IS NULL AND predecessor.sequence < candidate.sequence
		  )
		ORDER BY candidate.next_attempt_at,candidate.occurred_at,candidate.sequence,candidate.event_id
		FOR UPDATE SKIP LOCKED LIMIT 32
    )
    UPDATE catalog.outbox AS outbox SET leased_until=$2
    FROM candidates WHERE outbox.event_id=candidates.event_id
    RETURNING outbox.event_id,outbox.event_type,outbox.payload,outbox.attempts`, now, now.Add(lease))
	if err != nil {
		return nil, fmt.Errorf("catalog: claim outbox: %w", err)
	}
	defer rows.Close()
	events := make([]PendingOutboxEvent, 0, 32)
	for rows.Next() {
		var event PendingOutboxEvent
		if err = rows.Scan(&event.ID, &event.Type, &event.Payload, &event.Attempts); err != nil {
			return nil, fmt.Errorf("catalog: scan outbox: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (r *PostgresBookRepository) MarkPublished(ctx context.Context, id string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE catalog.outbox SET published_at=$2,leased_until=NULL WHERE event_id=$1`, id, now)
	return err
}

func (r *PostgresBookRepository) RetryOutbox(ctx context.Context, id string, now time.Time, attempt int) error {
	delay := time.Second << min(attempt, 8)
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	_, err := r.pool.Exec(ctx, `UPDATE catalog.outbox SET attempts=attempts+1,next_attempt_at=$2,leased_until=NULL WHERE event_id=$1`, id, now.Add(delay))
	return err
}

type rowScanner interface {
	Scan(...any) error
}

func scanBook(row rowScanner) (catalog.Book, error) {
	var book catalog.Book
	var checksum []byte
	err := row.Scan(&book.ID, &book.Metadata.Title, &book.Metadata.Author, &book.Metadata.Year, &book.Metadata.Tags,
		&book.ProcessingStatus, &book.CreatedAt, &book.ObjectReference, &checksum, &book.ByteSize,
		&book.ProcessingStage, &book.ProcessingFailureCategory, &book.ProcessingUpdatedAt, &book.ProcessingVersion)
	if err != nil {
		return catalog.Book{}, err
	}
	if len(checksum) != len(book.Checksum) {
		return catalog.Book{}, errors.New("catalog: invalid checksum in database")
	}
	copy(book.Checksum[:], checksum)
	return book, nil
}

type cursor struct {
	CreatedAt time.Time
	ID        string
}

type cursorToken struct {
	Version   int    `json:"v"`
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func decodeCursor(token string) (cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return cursor{}, err
	}
	var value cursorToken
	if err = json.Unmarshal(raw, &value); err != nil || value.Version != 1 || value.ID == "" {
		return cursor{}, errors.New("invalid cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, value.CreatedAt)
	if err != nil {
		return cursor{}, err
	}
	return cursor{CreatedAt: createdAt, ID: value.ID}, nil
}

func encodeCursor(book catalog.Book) string {
	raw, _ := json.Marshal(cursorToken{Version: 1, CreatedAt: book.CreatedAt.UTC().Format(time.RFC3339Nano), ID: book.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}
