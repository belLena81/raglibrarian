// Package repository contains Catalog's outward persistence adapters.
package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
)

// PostgresBookRepository persists a book and its outbox record in one transaction.
type PostgresBookRepository struct {
	pool *pgxpool.Pool
}

// PendingOutboxEvent is a leased event awaiting broker confirmation.
type PendingOutboxEvent struct {
	ID       string
	Type     string
	Payload  []byte
	Attempts int
}

func NewPostgresBookRepository(pool *pgxpool.Pool) *PostgresBookRepository {
	if pool == nil {
		panic("repository: pgx pool is required")
	}
	return &PostgresBookRepository{pool: pool}
}

func (r *PostgresBookRepository) Create(ctx context.Context, book catalog.Book, event catalog.OutboxEvent) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("catalog: begin create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `INSERT INTO catalog.books
        (id,title,author,year,tags,processing_status,created_at,object_reference,checksum,byte_size,media_type,actor_id)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'application/pdf',$11)`,
		book.ID, book.Metadata.Title, book.Metadata.Author, book.Metadata.Year, book.Metadata.Tags,
		string(book.ProcessingStatus), book.CreatedAt, book.ObjectReference, book.Checksum[:], book.ByteSize, book.ActorID)
	if err != nil {
		return fmt.Errorf("catalog: insert book: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO catalog.outbox
        (event_id,event_type,payload,occurred_at,next_attempt_at)
		VALUES ($1,$2,$3,$4,$4)`, event.ID, event.Type, event.Payload, event.OccurredAt)
	if err != nil {
		return fmt.Errorf("catalog: insert outbox: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("catalog: commit create: %w", err)
	}
	return nil
}

func (r *PostgresBookRepository) List(ctx context.Context, size int, token string) ([]catalog.Book, string, error) {
	args := []any{}
	query := `SELECT id,title,author,year,tags,processing_status,created_at,object_reference,checksum,byte_size
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
	book, err := scanBook(r.pool.QueryRow(ctx, `SELECT id,title,author,year,tags,processing_status,created_at,object_reference,checksum,byte_size
        FROM catalog.books WHERE id=$1 AND processing_status <> 'deleted'`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Book{}, catalog.ErrNotFound
	}
	if err != nil {
		return catalog.Book{}, fmt.Errorf("catalog: get book: %w", err)
	}
	return book, nil
}

// ClaimOutbox leases one due row. Processing one record at a time preserves a
// clear durable boundary between broker confirmation and publication marking.
func (r *PostgresBookRepository) ClaimOutbox(ctx context.Context, now time.Time, lease time.Duration) ([]PendingOutboxEvent, error) {
	rows, err := r.pool.Query(ctx, `WITH candidates AS (
        SELECT event_id FROM catalog.outbox
        WHERE published_at IS NULL AND next_attempt_at <= $1 AND (leased_until IS NULL OR leased_until < $1)
        ORDER BY next_attempt_at,event_id FOR UPDATE SKIP LOCKED LIMIT 1
    )
    UPDATE catalog.outbox AS outbox SET leased_until=$2
    FROM candidates WHERE outbox.event_id=candidates.event_id
    RETURNING outbox.event_id,outbox.event_type,outbox.payload,outbox.attempts`, now, now.Add(lease))
	if err != nil {
		return nil, fmt.Errorf("catalog: claim outbox: %w", err)
	}
	defer rows.Close()
	events := make([]PendingOutboxEvent, 0, 1)
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
		&book.ProcessingStatus, &book.CreatedAt, &book.ObjectReference, &checksum, &book.ByteSize)
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
	Version   int    `json:"v"`
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func decodeCursor(token string) (cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return cursor{}, err
	}
	var value cursor
	if err = json.Unmarshal(raw, &value); err != nil || value.Version != 1 || value.ID == "" {
		return cursor{}, errors.New("invalid cursor")
	}
	if _, err = time.Parse(time.RFC3339Nano, value.CreatedAt); err != nil {
		return cursor{}, err
	}
	return value, nil
}

func encodeCursor(book catalog.Book) string {
	raw, _ := json.Marshal(cursor{Version: 1, CreatedAt: book.CreatedAt.UTC().Format(time.RFC3339Nano), ID: book.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}
