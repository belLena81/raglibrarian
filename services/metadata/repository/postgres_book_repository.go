package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

const bookUniqueConstraint = "books_title_author_year_unique"

const (
	insertBookQuery = `
		INSERT INTO books (id, title, author, year, index_status, tags, s3_key, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	findBookByIDQuery = `
		SELECT id, title, author, year, index_status, tags, s3_key, created_at, updated_at
		FROM books WHERE id = $1`

	deleteBookQuery = `
		DELETE FROM books WHERE id = $1`

	updateS3KeyQuery = `
		UPDATE books SET s3_key = $1, updated_at = $2 WHERE id = $3`
)

// updateIndexStatusQuery uses a CTE to validate the transition before applying
// it, mirroring the domain state machine at the DB layer for defence in depth.
//
// The inner SELECT returns one row only when the current status is one of the
// allowed predecessors of $2. If no row is found the UPDATE touches nothing
// and we can distinguish "not found" from "bad transition" by checking whether
// the id exists in a second query.
//
// Allowed edges (mirrors domain.allowedTransitions):
//
//	pending  → indexing
//	indexing → indexed | failed
//	indexed  → pending
//	failed   → pending
const updateIndexStatusQuery = `
	WITH allowed AS (
		SELECT id FROM books
		WHERE id = $1
		  AND (
		        (index_status = 'pending'  AND $2 = 'indexing')
		     OR (index_status = 'indexing' AND $2 IN ('indexed', 'failed'))
		     OR (index_status = 'indexed'  AND $2 = 'pending')
		     OR (index_status = 'failed'   AND $2 = 'pending')
		  )
	)
	UPDATE books
	   SET index_status = $2,
	       updated_at   = $3
	 FROM allowed
	WHERE books.id = allowed.id`

// listBooksBaseQuery is extended dynamically with WHERE clauses.
const listBooksBaseQuery = `
	SELECT id, title, author, year, index_status, tags, s3_key, created_at, updated_at
	FROM books`

// PostgresBookRepository is the pgx/v5 implementation of BookRepository.
type PostgresBookRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresBookRepository constructs the repository. Panics if pool is nil.
func NewPostgresBookRepository(pool *pgxpool.Pool) *PostgresBookRepository {
	if pool == nil {
		panic("repository: pgxpool must not be nil")
	}
	return &PostgresBookRepository{pool: pool}
}

// Save inserts a new book row.
// Maps the (title, author, year) unique constraint violation to domain.ErrDuplicateBook.
func (r *PostgresBookRepository) Save(ctx context.Context, b domain.Book) error {
	var s3Key pgtype.Text
	if b.S3Key() != "" {
		s3Key = pgtype.Text{String: b.S3Key(), Valid: true}
	}

	_, err := r.pool.Exec(ctx, insertBookQuery,
		b.Id(),
		b.Title(),
		b.Author(),
		b.Year(),
		b.Status().String(),
		b.Tags(),
		s3Key,
		b.CreatedAt(),
		b.UpdatedAt(),
	)
	if err != nil {
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == pgUniqueViolation {
			if pgErr.ConstraintName == bookUniqueConstraint {
				return domain.ErrDuplicateBook
			}
		}
		return fmt.Errorf("repository: save book: %w", err)
	}
	return nil
}

// FindByID looks up a book by UUID. Returns domain.ErrBookNotFound when absent.
func (r *PostgresBookRepository) FindByID(ctx context.Context, id string) (domain.Book, error) {
	row := r.pool.QueryRow(ctx, findBookByIDQuery, id)
	b, err := scanBook(row)
	if err != nil {
		return domain.Book{}, err
	}
	return b, nil
}

// List returns books matching f. Returns an empty (non-nil) slice when none match.
func (r *PostgresBookRepository) List(ctx context.Context, f ListFilter) ([]domain.Book, error) {
	query, args := buildListQuery(f)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("repository: list books: %w", err)
	}
	defer rows.Close()

	books := make([]domain.Book, 0)
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		books = append(books, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repository: list books iterate: %w", err)
	}
	return books, nil
}

// Delete removes a book by ID. Returns domain.ErrBookNotFound when absent.
func (r *PostgresBookRepository) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, deleteBookQuery, id)
	if err != nil {
		return fmt.Errorf("repository: delete book: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrBookNotFound
	}
	return nil
}

// UpdateStatus advances the index_status column enforcing the domain state
// machine via a CTE transition guard at the DB level.
// Returns domain.ErrBookNotFound when the ID is absent.
// Returns domain.ErrInvalidStatusTransition when the edge is forbidden.
func (r *PostgresBookRepository) UpdateStatus(ctx context.Context, id string, next domain.Status) error {
	tag, err := r.pool.Exec(ctx, updateIndexStatusQuery, id, next.String(), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("repository: update index status: %w", err)
	}

	if tag.RowsAffected() == 0 {
		// The CTE matched nothing. Determine which guard fired.
		var exists bool
		err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM books WHERE id = $1)`, id).Scan(&exists)
		if err != nil {
			return fmt.Errorf("repository: update index status existence check: %w", err)
		}
		if !exists {
			return domain.ErrBookNotFound
		}
		return domain.ErrInvalidStatusTransition
	}
	return nil
}

// UpdateS3Key sets the s3_key column. Returns domain.ErrBookNotFound when absent.
func (r *PostgresBookRepository) UpdateS3Key(ctx context.Context, id, s3Key string) error {
	if strings.TrimSpace(s3Key) == "" {
		return domain.ErrEmptyS3Key
	}
	tag, err := r.pool.Exec(ctx, updateS3KeyQuery, s3Key, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("repository: update s3 key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrBookNotFound
	}
	return nil
}

// ── Query builder ─────────────────────────────────────────────────────────────

// buildListQuery constructs a parameterised SELECT for the given filter.
// All predicates use positional parameters — no string interpolation of
// user-supplied values.
func buildListQuery(f ListFilter) (string, []any) {
	var (
		clauses []string
		args    []any
		n       = 1 // next positional parameter index
	)

	if f.Author != nil {
		clauses = append(clauses, fmt.Sprintf("author = $%d", n))
		args = append(args, *f.Author)
		n++
	}
	if f.YearFrom != nil {
		clauses = append(clauses, fmt.Sprintf("year >= $%d", n))
		args = append(args, *f.YearFrom)
		n++
	}
	if f.YearTo != nil {
		clauses = append(clauses, fmt.Sprintf("year <= $%d", n))
		args = append(args, *f.YearTo)
		n++
	}
	if f.Status != nil {
		clauses = append(clauses, fmt.Sprintf("index_status = $%d", n))
		args = append(args, f.Status.String())
		n++
	}
	// Tags: all supplied tags must appear in the book's tags array.
	// @> is the Postgres array containment operator and uses the GIN index.
	if len(f.Tags) > 0 {
		clauses = append(clauses, fmt.Sprintf("tags @> $%d", n))
		args = append(args, f.Tags)
	}

	query := listBooksBaseQuery
	if len(clauses) > 0 {
		query += "\n\t WHERE " + strings.Join(clauses, "\n\t   AND ")
	}
	query += "\n\t ORDER BY created_at DESC"

	return query, args
}

// ── Scanner ───────────────────────────────────────────────────────────────────

// pgxScanner is satisfied by both pgx.Row and pgx.Rows so scanBook works for
// single-row queries (QueryRow) and multi-row iteration (Rows.Scan).
type pgxScanner interface {
	Scan(dest ...any) error
}

func scanBook(row pgxScanner) (domain.Book, error) {
	var (
		id        string
		title     string
		author    string
		year      int
		statusStr string
		tags      []string
		s3KeyText pgtype.Text
		createdAt time.Time
		updatedAt time.Time
	)

	if err := row.Scan(
		&id, &title, &author, &year,
		&statusStr, &tags, &s3KeyText,
		&createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Book{}, domain.ErrBookNotFound
		}
		return domain.Book{}, fmt.Errorf("repository: scan book: %w", err)
	}

	status, err := domain.StatusValueOf(statusStr)
	if err != nil {
		return domain.Book{}, fmt.Errorf("repository: unrecognised index_status %q in database: %w", statusStr, err)
	}

	var s3Key string
	if s3KeyText.Valid {
		s3Key = s3KeyText.String
	}

	if tags == nil {
		tags = []string{}
	}

	return domain.NewBookFromDB(id, title, author, year, status, tags, s3Key, createdAt, updatedAt), nil
}
