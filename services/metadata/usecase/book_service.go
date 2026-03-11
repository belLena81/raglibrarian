package usecase

import (
	"context"
	"fmt"
	"strings"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/metadata/repository"
)

// BookUseCase is the application-layer contract for book management.
// It is the boundary callers outside this package depend on; the concrete
// BookService struct is an implementation detail wired in cmd/main.go.
type BookUseCase interface {
	// AddBook validates and persists a new book, returning it in StatusPending.
	// Returns domain.ErrEmptyTitle, ErrEmptyAuthor, ErrInvalidYear on bad input.
	// Returns domain.ErrDuplicateBook on (title, author, year) conflict.
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
	TriggerReindex(ctx context.Context, id string) error
}

// BookService is the production implementation of BookUseCase.
type BookService struct {
	books repository.BookRepository
}

// NewBookService constructs a BookService. Panics if books is nil.
func NewBookService(books repository.BookRepository) *BookService {
	if books == nil {
		panic("usecase: BookRepository must not be nil")
	}
	return &BookService{books: books}
}

// AddBook validates, constructs, and persists a new Book in StatusPending.
func (s *BookService) AddBook(ctx context.Context, title, author string, year int) (domain.Book, error) {
	book, err := domain.NewBook(title, author, year)
	if err != nil {
		return domain.Book{}, err
	}

	if err = s.books.Save(ctx, book); err != nil {
		return domain.Book{}, err
	}

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

// TriggerReindex resets a terminal book back to StatusPending.
// It delegates to UpdateIndexStatus so all guards (empty id, not found,
// forbidden transition) are enforced consistently.
func (s *BookService) TriggerReindex(ctx context.Context, id string) error {
	return s.UpdateStatus(ctx, id, domain.Pending)
}
