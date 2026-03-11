package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/metadata/publisher"
	"github.com/belLena81/raglibrarian/services/metadata/repository"
)

// BookUseCase is the application-layer contract for book management.
// It is the boundary callers outside this package depend on; the concrete
// BookService struct is an implementation detail wired in cmd/main.go.
type BookUseCase interface {
	// AddBook validates and persists a new book, returning it in StatusPending.
	// Returns domain.ErrEmptyTitle, ErrEmptyAuthor, ErrInvalidYear on bad input.
	// Returns domain.ErrDuplicateBook on (title, author, year) conflict.
	// Publishes EventBookCreated after a successful save.
	AddBook(ctx context.Context, title, author string, year int) (domain.Book, error)

	// GetBook returns the book with the given ID.
	// Returns domain.ErrEmptyBookID for a blank id.
	// Returns domain.ErrBookNotFound when absent.
	GetBook(ctx context.Context, id string) (domain.Book, error)

	// ListBooks returns all books matching the filter. Never returns nil.
	ListBooks(ctx context.Context, f repository.ListFilter) ([]domain.Book, error)

	// RemoveBook hard-deletes a book by ID.
	// Returns domain.ErrEmptyBookID for a blank id.
	// Returns domain.ErrBookNotFound when absent.
	RemoveBook(ctx context.Context, id string) error

	// UpdateStatus advances the index pipeline state.
	// Returns domain.ErrEmptyBookID for a blank id.
	// Returns domain.ErrInvalidStatus for an unrecognised status value.
	// Returns domain.ErrInvalidStatusTransition for a forbidden edge.
	// Returns domain.ErrBookNotFound when absent.
	UpdateStatus(ctx context.Context, id string, next domain.Status) error

	// TriggerReindex resets a terminal book (indexed or failed) back to pending
	// so the ingest pipeline will pick it up again.
	// Returns domain.ErrEmptyBookID for a blank id.
	// Returns domain.ErrInvalidStatusTransition when the book is not in a
	// terminal state (pending or indexing cannot be reindexed).
	// Returns domain.ErrBookNotFound when absent.
	// Publishes EventBookReindexRequested after a successful status transition.
	TriggerReindex(ctx context.Context, id string) error
}

// BookService is the production implementation of BookUseCase.
type BookService struct {
	books repository.BookRepository
	// pub is optional: nil means no events are published (used in tests and
	// deployments where a broker is not available). The interface is checked
	// rather than the concrete type so any fake satisfies it.
	pub publisher.BookPublisher
}

// NewBookService constructs a BookService. Panics if books is nil.
// pub may be nil; when nil, domain events are silently skipped.
func NewBookService(books repository.BookRepository, pub publisher.BookPublisher) *BookService {
	if books == nil {
		panic("usecase: BookRepository must not be nil")
	}
	return &BookService{books: books, pub: pub}
}

// AddBook validates, constructs, and persists a new Book in StatusPending.
// On success it publishes EventBookCreated. A publish failure is logged as a
// warning but does not roll back the save — the book record is the source of
// truth; the event is best-effort for now.
func (s *BookService) AddBook(ctx context.Context, title, author string, year int) (domain.Book, error) {
	book, err := domain.NewBook(title, author, year)
	if err != nil {
		return domain.Book{}, err
	}

	if err = s.books.Save(ctx, book); err != nil {
		return domain.Book{}, err
	}

	s.publish(ctx, publisher.BookEvent{
		Event:      publisher.EventBookCreated,
		BookID:     book.Id(),
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
	})

	return book, nil
}

// GetBook returns the Book with the given id.
func (s *BookService) GetBook(ctx context.Context, id string) (domain.Book, error) {
	if strings.TrimSpace(id) == "" {
		return domain.Book{}, domain.ErrEmptyBookID
	}

	book, err := s.books.FindByID(ctx, id)
	if err != nil {
		return domain.Book{}, err
	}

	return book, nil
}

// ListBooks returns all books matching f. The returned slice is never nil.
func (s *BookService) ListBooks(ctx context.Context, f repository.ListFilter) ([]domain.Book, error) {
	books, err := s.books.List(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("usecase: list books: %w", err)
	}

	if books == nil {
		books = []domain.Book{}
	}

	return books, nil
}

// RemoveBook hard-deletes the book with the given id.
func (s *BookService) RemoveBook(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return domain.ErrEmptyBookID
	}

	return s.books.Delete(ctx, id)
}

// UpdateStatus advances the pipeline state for the given book.
// It validates the status value against the domain before touching the repo,
// so callers always receive a domain sentinel rather than a wrapped repo error.
func (s *BookService) UpdateStatus(ctx context.Context, id string, next domain.Status) error {
	if strings.TrimSpace(id) == "" {
		return domain.ErrEmptyBookID
	}

	// Guard unrecognised values before the repo call so the error is always
	// a clean domain sentinel regardless of which repo implementation is wired.
	if !next.IsValid() {
		return domain.ErrInvalidStatus
	}

	return s.books.UpdateStatus(ctx, id, next)
}

// TriggerReindex resets a terminal book back to StatusPending and publishes
// EventBookReindexRequested. The book's s3_key is fetched first so the Lambda
// can locate the existing PDF without a separate lookup.
// Delegates to UpdateIndexStatus for all guards (empty id, not found, forbidden
// transition) rather than duplicating them.
func (s *BookService) TriggerReindex(ctx context.Context, id string) error {
	// Fetch before status update so we have the s3_key for the event payload.
	// If the book does not exist, UpdateIndexStatus will return ErrBookNotFound
	// regardless — we tolerate the extra lookup for the cleaner event payload.
	book, err := s.GetBook(ctx, id)
	if err != nil {
		return err
	}

	if err = s.UpdateStatus(ctx, id, domain.Pending); err != nil {
		return err
	}

	s.publish(ctx, publisher.BookEvent{
		Event:      publisher.EventBookReindexRequested,
		BookID:     book.Id(),
		S3Key:      book.S3Key(),
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
	})

	return nil
}

// publish sends an event if a publisher is configured. Failures are swallowed
// here: the database write has already succeeded and rolling it back would
// require a saga. A real deployment should add structured logging and/or a
// dead-letter mechanism in this helper.
//
// Decision: best-effort publish at the use-case layer rather than transactional
// outbox. An outbox (polling the DB) is the correct solution for guaranteed
// delivery but requires a background worker and schema migration. The current
// trade-off is deliberate and documented for Step N (outbox / retry).
func (s *BookService) publish(ctx context.Context, event publisher.BookEvent) {
	if s.pub == nil {
		return
	}
	// Ignore publish errors for now — see doc comment above.
	_ = s.pub.Publish(ctx, event)
}
