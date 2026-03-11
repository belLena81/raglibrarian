package repository

import (
	"context"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// ListFilter constrains which books are returned by List.
// All fields are optional; nil means unconstrained.
type ListFilter struct {
	Author   *string
	YearFrom *int
	YearTo   *int
	Tags     []string // books must contain ALL supplied tags
	Status   *domain.Status
}

// BookRepository is the persistence port for the Book aggregate.
type BookRepository interface {
	// Save persists a new Book.
	// Returns domain.ErrDuplicateBook on (title, author, year) conflict.
	Save(ctx context.Context, book domain.Book) error

	// FindByID returns the Book with the given ID.
	// Returns domain.ErrBookNotFound if absent.
	FindByID(ctx context.Context, id string) (domain.Book, error)

	// List returns all books matching f. Never returns nil.
	List(ctx context.Context, f ListFilter) ([]domain.Book, error)

	// Delete removes a book by ID.
	// Returns domain.ErrBookNotFound if absent.
	Delete(ctx context.Context, id string) error

	// UpdateStatus advances the index_status column, enforcing the
	// domain state machine at the DB level via a CTE transition guard.
	// Returns domain.ErrBookNotFound if absent.
	// Returns domain.ErrInvalidStatusTransition if the edge is forbidden.
	UpdateStatus(ctx context.Context, id string, next domain.Status) error

	// UpdateS3Key sets the s3_key column for the given book.
	// Returns domain.ErrBookNotFound if absent.
	UpdateS3Key(ctx context.Context, id, s3Key string) error
}
