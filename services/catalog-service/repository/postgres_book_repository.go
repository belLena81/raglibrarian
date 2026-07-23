// Package repository contains Catalog's outward persistence adapters.
package repository

import (
	"bytes"
	"context"
	"database/sql"
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
		 processing_stage,processing_failure_category,processing_updated_at,processing_version,lifecycle_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		book.ID, book.Metadata.Title, book.Metadata.Author, book.Metadata.Year, book.Metadata.Tags,
		string(book.ProcessingStatus), book.CreatedAt, book.ObjectReference, book.Checksum[:], book.ByteSize, book.MediaType, book.ActorID,
		string(book.ProcessingStage), string(book.ProcessingFailureCategory), book.ProcessingUpdatedAt, book.ProcessingVersion,
		book.LifecycleVersion)
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
		media_type,processing_stage,processing_failure_category,processing_updated_at,processing_version,lifecycle_version,
		manifest_reference,manifest_sha256,lifecycle_command_id,original_deleted,artifacts_deleted,index_deleted
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
		media_type,processing_stage,processing_failure_category,processing_updated_at,processing_version,lifecycle_version,
		manifest_reference,manifest_sha256,lifecycle_command_id,original_deleted,artifacts_deleted,index_deleted
        FROM catalog.books WHERE id=$1 AND processing_status <> 'deleted'`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Book{}, catalog.ErrNotFound
	}
	if err != nil {
		return catalog.Book{}, fmt.Errorf("catalog: get book: %w", err)
	}
	return book, nil
}

// ApplyProcessingEvent deduplicates a processing fact, updates the Book
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
		media_type,processing_stage,processing_failure_category,processing_updated_at,processing_version,lifecycle_version,
		manifest_reference,manifest_sha256,lifecycle_command_id,original_deleted,artifacts_deleted,index_deleted
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
	if event.LifecycleVersion != book.LifecycleVersion {
		return catalog.Book{}, false, catalog.ErrProcessingEventConflict
	}
	changed, err := book.ApplyProcessingFact(event.Fact)
	if err != nil {
		return catalog.Book{}, false, err
	}
	if changed {
		if event.Fact.Kind == catalog.ProcessingChunksReady {
			book.ManifestReference = event.ManifestReference
			book.ManifestChecksum = event.ManifestSHA256
		}
		var manifestReference any
		var manifestChecksum any
		if book.ManifestReference != "" {
			manifestReference = book.ManifestReference
			manifestChecksum = book.ManifestChecksum[:]
		}
		payload, marshalErr := proto.Marshal(&catalogv1.BookProcessingStatusChangedV1{
			EventId: statusEventID, BookId: book.ID, ProcessingStatus: string(book.ProcessingStatus),
			ProcessingStage: string(book.ProcessingStage), ProcessingFailureCategory: string(book.ProcessingFailureCategory),
			ProcessingVersion: book.ProcessingVersion, UpdatedAt: timestamppb.New(book.ProcessingUpdatedAt),
			CorrelationId: event.CorrelationID, OccurredAt: timestamppb.New(appliedAt), CausationId: event.EventID,
			Producer: "catalog-service", SchemaVersion: "v1",
			IdempotencyKey:   fmt.Sprintf("%s:processing:%d", book.ID, book.ProcessingVersion),
			LifecycleVersion: book.LifecycleVersion, CanReindex: book.CanReindex(),
		})
		if marshalErr != nil {
			return catalog.Book{}, false, errors.New("catalog: status event unavailable")
		}
		if _, err = tx.Exec(ctx, `UPDATE catalog.books SET processing_status=$2,processing_stage=$3,
			processing_failure_category=$4,processing_updated_at=$5,processing_version=$6,
			manifest_reference=$7,manifest_sha256=$8 WHERE id=$1`,
			book.ID, string(book.ProcessingStatus), string(book.ProcessingStage), string(book.ProcessingFailureCategory),
			book.ProcessingUpdatedAt, book.ProcessingVersion, manifestReference, manifestChecksum); err != nil {
			return catalog.Book{}, false, fmt.Errorf("catalog: update processing book: %w", err)
		}
		var sequence int64
		if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),-1)+1 FROM catalog.outbox WHERE aggregate_id=$1`, book.ID).Scan(&sequence); err != nil {
			return catalog.Book{}, false, fmt.Errorf("catalog: allocate processing outbox sequence: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO catalog.outbox
			(event_id,event_type,aggregate_id,sequence,payload,occurred_at,next_attempt_at)
			VALUES ($1,'catalog.book.processing-status-changed.v1',$2,$3,$4,$5,$5)`, statusEventID, book.ID, sequence, payload, appliedAt); err != nil {
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

func (r *PostgresBookRepository) ApplyLifecycleCommand(
	ctx context.Context,
	command catalog.LifecycleCommand,
) (catalog.Book, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: begin lifecycle command: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	book, err := scanBook(tx.QueryRow(ctx, lifecycleBookSelect+`
		WHERE id=$1 FOR UPDATE`, command.BookID))
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Book{}, false, catalog.ErrNotFound
	}
	if err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: lock lifecycle book: %w", err)
	}
	var existingBookID, existingType string
	var existingVersion int64
	err = tx.QueryRow(ctx, `SELECT book_id,command_type,lifecycle_version
		FROM catalog.lifecycle_commands WHERE command_id=$1`, command.CommandID).
		Scan(&existingBookID, &existingType, &existingVersion)
	if err == nil {
		if existingBookID != command.BookID || existingType != string(command.Kind) {
			return catalog.Book{}, false, catalog.ErrInvalidCommand
		}
		if book.LifecycleVersion != existingVersion {
			return catalog.Book{}, false, catalog.ErrProcessingEventConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return catalog.Book{}, false, fmt.Errorf("catalog: commit duplicate lifecycle command: %w", err)
		}
		return book, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return catalog.Book{}, false, fmt.Errorf("catalog: read lifecycle command: %w", err)
	}
	if book.ProcessingStatus == catalog.BookStatusDeleted {
		return catalog.Book{}, false, catalog.ErrNotFound
	}

	switch command.Kind {
	case catalog.LifecycleCommandReindex:
		if !book.CanReindex() {
			return catalog.Book{}, false, catalog.ErrInvalidTransition
		}
		if err = book.TransitionTo(catalog.BookStatusReindexing); err != nil {
			return catalog.Book{}, false, err
		}
		book.ProcessingStage = catalog.BookStageChunksReady
		book.ProcessingFailureCategory = ""
	case catalog.LifecycleCommandDelete:
		if err = book.TransitionTo(catalog.BookStatusDeleting); err != nil {
			return catalog.Book{}, false, err
		}
		book.OriginalDeleted = false
		book.ArtifactsDeleted = false
		book.IndexDeleted = false
	default:
		return catalog.Book{}, false, catalog.ErrInvalidCommand
	}
	book.LifecycleVersion++
	book.ProcessingVersion++
	book.ProcessingUpdatedAt = command.OccurredAt
	book.DeleteCommandID = command.CommandID

	if _, err = tx.Exec(ctx, `UPDATE catalog.books SET processing_status=$2,processing_stage=$3,
		processing_failure_category=$4,processing_updated_at=$5,processing_version=$6,lifecycle_version=$7,
		lifecycle_command_id=$8,original_deleted=$9,artifacts_deleted=$10,index_deleted=$11 WHERE id=$1`,
		book.ID, string(book.ProcessingStatus), string(book.ProcessingStage), string(book.ProcessingFailureCategory),
		book.ProcessingUpdatedAt, book.ProcessingVersion, book.LifecycleVersion, book.DeleteCommandID,
		book.OriginalDeleted, book.ArtifactsDeleted, book.IndexDeleted); err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: update lifecycle book: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO catalog.lifecycle_commands
		(command_id,book_id,command_type,lifecycle_version,actor_id,correlation_id,accepted_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, command.CommandID, book.ID, string(command.Kind),
		book.LifecycleVersion, command.ActorID, command.CorrelationID, command.OccurredAt); err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: insert lifecycle command: %w", err)
	}
	statusPayload, err := proto.Marshal(&catalogv1.BookProcessingStatusChangedV1{
		EventId: command.StatusEventID, BookId: book.ID, ProcessingStatus: string(book.ProcessingStatus),
		ProcessingStage: string(book.ProcessingStage), ProcessingFailureCategory: string(book.ProcessingFailureCategory),
		ProcessingVersion: book.ProcessingVersion, UpdatedAt: timestamppb.New(book.ProcessingUpdatedAt),
		CorrelationId: command.CorrelationID, OccurredAt: timestamppb.New(command.OccurredAt),
		CausationId: command.CommandID, Producer: "catalog-service", SchemaVersion: "v1",
		IdempotencyKey:   fmt.Sprintf("%s:processing:%d", book.ID, book.ProcessingVersion),
		LifecycleVersion: book.LifecycleVersion, CanReindex: book.CanReindex(),
	})
	if err != nil {
		return catalog.Book{}, false, errors.New("catalog: lifecycle status event unavailable")
	}
	var requestPayload []byte
	var requestType string
	switch command.Kind {
	case catalog.LifecycleCommandReindex:
		requestType = "catalog.book.reindex-requested.v1"
		requestPayload, err = proto.Marshal(&catalogv1.BookReindexRequestedV1{
			EventId: command.EventID, BookId: book.ID, CommandId: command.CommandID,
			LifecycleVersion: book.LifecycleVersion, SourceSha256: book.Checksum[:],
			ManifestReference: book.ManifestReference, ManifestSha256: book.ManifestChecksum[:],
			ActorId: command.ActorID, CorrelationId: command.CorrelationID,
			OccurredAt: timestamppb.New(command.OccurredAt), CausationId: command.CommandID,
			Producer: "catalog-service", SchemaVersion: "v1", IdempotencyKey: command.CommandID,
		})
	case catalog.LifecycleCommandDelete:
		requestType = "catalog.book.deletion-requested.v1"
		requestPayload, err = proto.Marshal(&catalogv1.BookDeletionRequestedV1{
			EventId: command.EventID, BookId: book.ID, CommandId: command.CommandID,
			LifecycleVersion: book.LifecycleVersion, ActorId: command.ActorID,
			CorrelationId: command.CorrelationID, OccurredAt: timestamppb.New(command.OccurredAt),
			CausationId: command.CommandID, Producer: "catalog-service",
			SchemaVersion: "v1", IdempotencyKey: command.CommandID,
		})
	}
	if err != nil {
		return catalog.Book{}, false, errors.New("catalog: lifecycle request event unavailable")
	}
	var sequence int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),-1)+1
		FROM catalog.outbox WHERE aggregate_id=$1`, book.ID).Scan(&sequence); err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: allocate lifecycle outbox sequence: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO catalog.outbox
		(event_id,event_type,aggregate_id,sequence,payload,occurred_at,next_attempt_at)
		VALUES ($1,$2,$3,$4,$5,$6,$6),($7,'catalog.book.processing-status-changed.v1',$3,$8,$9,$6,$6)`,
		command.EventID, requestType, book.ID, sequence, requestPayload, command.OccurredAt,
		command.StatusEventID, sequence+1, statusPayload); err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: insert lifecycle outbox: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: commit lifecycle command: %w", err)
	}
	r.wakeOutbox()
	return book, true, nil
}

const lifecycleBookSelect = `SELECT id,title,author,year,tags,processing_status,created_at,
	object_reference,checksum,byte_size,media_type,processing_stage,processing_failure_category,
	processing_updated_at,processing_version,lifecycle_version,manifest_reference,manifest_sha256,
	lifecycle_command_id,original_deleted,artifacts_deleted,index_deleted FROM catalog.books`

func (r *PostgresBookRepository) MarkOriginalDeleted(
	ctx context.Context,
	bookID string,
	commandID string,
	lifecycleVersion int64,
	appliedAt time.Time,
) (catalog.Book, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return catalog.Book{}, fmt.Errorf("catalog: begin original deletion acknowledgement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	book, err := scanBook(tx.QueryRow(ctx, lifecycleBookSelect+` WHERE id=$1 FOR UPDATE`, bookID))
	if err != nil {
		return catalog.Book{}, fmt.Errorf("catalog: lock original deletion book: %w", err)
	}
	if book.DeleteCommandID != commandID || book.LifecycleVersion != lifecycleVersion ||
		(book.ProcessingStatus != catalog.BookStatusDeleting && book.ProcessingStatus != catalog.BookStatusDeleted) {
		return catalog.Book{}, catalog.ErrProcessingEventConflict
	}
	if book.OriginalDeleted {
		if err = tx.Commit(ctx); err != nil {
			return catalog.Book{}, fmt.Errorf("catalog: commit duplicate original deletion acknowledgement: %w", err)
		}
		return book, nil
	}
	book.OriginalDeleted = true
	finalized := book.ArtifactsDeleted && book.IndexDeleted
	if finalized {
		if err = r.finalizeDeletion(ctx, tx, &book, appliedAt); err != nil {
			return catalog.Book{}, err
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE catalog.books SET original_deleted=TRUE WHERE id=$1`, book.ID)
	}
	if err != nil {
		return catalog.Book{}, fmt.Errorf("catalog: mark original deleted: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return catalog.Book{}, fmt.Errorf("catalog: commit original deletion acknowledgement: %w", err)
	}
	if finalized {
		r.wakeOutbox()
	}
	return book, nil
}

func (r *PostgresBookRepository) PendingOriginalDeletions(
	ctx context.Context,
	limit int,
) ([]catalog.PendingOriginalDeletion, error) {
	if limit < 1 || limit > 100 {
		return nil, catalog.ErrInvalidPagination
	}
	rows, err := r.pool.Query(ctx, `SELECT id,lifecycle_command_id,lifecycle_version,object_reference
		FROM catalog.books WHERE processing_status='deleting' AND original_deleted=FALSE
		ORDER BY processing_updated_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("catalog: list pending original deletions: %w", err)
	}
	defer rows.Close()
	deletions := make([]catalog.PendingOriginalDeletion, 0, limit)
	for rows.Next() {
		var deletion catalog.PendingOriginalDeletion
		if err = rows.Scan(&deletion.BookID, &deletion.CommandID, &deletion.LifecycleVersion, &deletion.ObjectReference); err != nil {
			return nil, fmt.Errorf("catalog: scan pending original deletion: %w", err)
		}
		deletions = append(deletions, deletion)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: pending original deletion rows: %w", err)
	}
	return deletions, nil
}

func (r *PostgresBookRepository) ApplyLifecycleAck(
	ctx context.Context,
	ack catalog.LifecycleAck,
	appliedAt time.Time,
) (catalog.Book, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: begin lifecycle acknowledgement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `INSERT INTO catalog.lifecycle_inbox
		(event_id,event_type,payload_sha256,processed_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (event_id) DO NOTHING`, ack.EventID, ack.EventType, ack.PayloadSHA256[:], appliedAt)
	if err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: insert lifecycle acknowledgement: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var existingType string
		var existingDigest []byte
		if err = tx.QueryRow(ctx, `SELECT event_type,payload_sha256 FROM catalog.lifecycle_inbox
			WHERE event_id=$1`, ack.EventID).Scan(&existingType, &existingDigest); err != nil {
			return catalog.Book{}, false, fmt.Errorf("catalog: read lifecycle acknowledgement: %w", err)
		}
		if existingType != ack.EventType || !bytes.Equal(existingDigest, ack.PayloadSHA256[:]) {
			return catalog.Book{}, false, catalog.ErrProcessingEventConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return catalog.Book{}, false, fmt.Errorf("catalog: commit duplicate lifecycle acknowledgement: %w", err)
		}
		return catalog.Book{}, false, nil
	}
	book, err := scanBook(tx.QueryRow(ctx, lifecycleBookSelect+` WHERE id=$1 FOR UPDATE`, ack.BookID))
	if err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: lock lifecycle acknowledgement book: %w", err)
	}
	if book.DeleteCommandID != ack.CommandID || book.LifecycleVersion != ack.LifecycleVersion ||
		(book.ProcessingStatus != catalog.BookStatusDeleting && book.ProcessingStatus != catalog.BookStatusDeleted) {
		return catalog.Book{}, false, catalog.ErrProcessingEventConflict
	}
	changed := false
	switch ack.EventType {
	case "ingestion.book.artifacts-deleted.v1":
		if !book.ArtifactsDeleted {
			book.ArtifactsDeleted = true
			changed = true
		}
	case "retrieval.book.index-deleted.v1":
		if !book.IndexDeleted {
			book.IndexDeleted = true
			changed = true
		}
	default:
		return catalog.Book{}, false, catalog.ErrInvalidProcessingEvent
	}
	finalized := changed && book.OriginalDeleted && book.ArtifactsDeleted && book.IndexDeleted
	if finalized {
		if err = r.finalizeDeletion(ctx, tx, &book, appliedAt); err != nil {
			return catalog.Book{}, false, err
		}
	} else if changed {
		_, err = tx.Exec(ctx, `UPDATE catalog.books SET artifacts_deleted=$2,index_deleted=$3 WHERE id=$1`,
			book.ID, book.ArtifactsDeleted, book.IndexDeleted)
		if err != nil {
			return catalog.Book{}, false, fmt.Errorf("catalog: apply lifecycle acknowledgement: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return catalog.Book{}, false, fmt.Errorf("catalog: commit lifecycle acknowledgement: %w", err)
	}
	if finalized {
		r.wakeOutbox()
	}
	return book, changed, nil
}

func (r *PostgresBookRepository) finalizeDeletion(
	ctx context.Context,
	tx pgx.Tx,
	book *catalog.Book,
	appliedAt time.Time,
) error {
	var correlationID sql.NullString
	if err := tx.QueryRow(ctx, `SELECT correlation_id FROM catalog.lifecycle_commands
		WHERE command_id=$1`, book.DeleteCommandID).Scan(&correlationID); err != nil {
		return fmt.Errorf("catalog: read deletion correlation: %w", err)
	}
	book.ProcessingStatus = catalog.BookStatusDeleted
	book.ProcessingVersion++
	book.ProcessingUpdatedAt = appliedAt.UTC()
	book.Metadata = catalog.BookMetadata{}
	book.ObjectReference = ""
	book.Checksum = [32]byte{}
	book.ByteSize = 0
	book.MediaType = ""
	book.ActorID = ""
	book.ProcessingStage = ""
	book.ProcessingFailureCategory = ""
	book.ManifestReference = ""
	book.ManifestChecksum = [32]byte{}
	if _, err := tx.Exec(ctx, `UPDATE catalog.books SET
		title=NULL,author=NULL,year=NULL,tags=NULL,object_reference=NULL,checksum=NULL,
		byte_size=NULL,media_type=NULL,actor_id=NULL,manifest_reference=NULL,manifest_sha256=NULL,
		processing_status='deleted',processing_stage=NULL,processing_failure_category=NULL,
		processing_version=$2,processing_updated_at=$3,original_deleted=$4,
		artifacts_deleted=$5,index_deleted=$6 WHERE id=$1`,
		book.ID, book.ProcessingVersion, book.ProcessingUpdatedAt, book.OriginalDeleted,
		book.ArtifactsDeleted, book.IndexDeleted); err != nil {
		return fmt.Errorf("catalog: finalize deletion tombstone: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE catalog.lifecycle_commands
		SET actor_id=NULL,correlation_id=NULL WHERE book_id=$1`, book.ID); err != nil {
		return fmt.Errorf("catalog: purge lifecycle command principals: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM catalog.outbox WHERE aggregate_id=$1`, book.ID); err != nil {
		return fmt.Errorf("catalog: purge lifecycle outbox payloads: %w", err)
	}
	eventID := book.DeleteCommandID + ":deleted"
	payload, err := proto.Marshal(&catalogv1.BookProcessingStatusChangedV1{
		EventId: eventID, BookId: book.ID, ProcessingStatus: string(book.ProcessingStatus),
		ProcessingVersion: book.ProcessingVersion, UpdatedAt: timestamppb.New(book.ProcessingUpdatedAt),
		CorrelationId: correlationID.String, OccurredAt: timestamppb.New(book.ProcessingUpdatedAt),
		CausationId: book.DeleteCommandID, Producer: "catalog-service", SchemaVersion: "v1",
		IdempotencyKey:   fmt.Sprintf("%s:processing:%d", book.ID, book.ProcessingVersion),
		LifecycleVersion: book.LifecycleVersion, CanReindex: false,
	})
	if err != nil {
		return errors.New("catalog: final deletion status event unavailable")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO catalog.outbox
		(event_id,event_type,aggregate_id,sequence,payload,occurred_at,next_attempt_at)
		VALUES ($1,'catalog.book.processing-status-changed.v1',$2,0,$3,$4,$4)`,
		eventID, book.ID, payload, book.ProcessingUpdatedAt); err != nil {
		return fmt.Errorf("catalog: insert final deletion status outbox: %w", err)
	}
	return nil
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
	var manifestChecksum []byte
	var title, author, objectReference, mediaType sql.NullString
	var processingStage, processingFailureCategory, manifestReference sql.NullString
	var year, byteSize sql.NullInt64
	err := row.Scan(&book.ID, &title, &author, &year, &book.Metadata.Tags,
		&book.ProcessingStatus, &book.CreatedAt, &objectReference, &checksum, &byteSize,
		&mediaType, &processingStage, &processingFailureCategory, &book.ProcessingUpdatedAt,
		&book.ProcessingVersion, &book.LifecycleVersion, &manifestReference, &manifestChecksum,
		&book.DeleteCommandID, &book.OriginalDeleted, &book.ArtifactsDeleted, &book.IndexDeleted)
	if err != nil {
		return catalog.Book{}, err
	}
	book.Metadata.Title = title.String
	book.Metadata.Author = author.String
	book.Metadata.Year = int(year.Int64)
	book.ObjectReference = objectReference.String
	book.ByteSize = byteSize.Int64
	book.MediaType = mediaType.String
	book.ProcessingStage = catalog.BookProcessingStage(processingStage.String)
	book.ProcessingFailureCategory = catalog.ProcessingFailureCategory(processingFailureCategory.String)
	book.ManifestReference = manifestReference.String
	if book.ProcessingStatus != catalog.BookStatusDeleted && len(checksum) != len(book.Checksum) {
		return catalog.Book{}, errors.New("catalog: invalid checksum in database")
	}
	copy(book.Checksum[:], checksum)
	if len(manifestChecksum) != 0 && len(manifestChecksum) != len(book.ManifestChecksum) {
		return catalog.Book{}, errors.New("catalog: invalid manifest checksum in database")
	}
	copy(book.ManifestChecksum[:], manifestChecksum)
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
