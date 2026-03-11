// Package domain contains the core business entities and rules for raglibrarian.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Status represents the ingestion pipeline state of a Book.
type Status int

// Statuses for the ingestion pipeline
const (
	Pending Status = iota
	Indexing
	Indexed
	Failed
)

// allowedTransitions defines the legal state machine edges.
// pending → indexing → indexed|failed; indexed|failed → pending (reindex).
var allowedTransitions = map[Status][]Status{
	Pending:  {Indexing},
	Indexing: {Indexed, Failed},
	Indexed:  {Pending},
	Failed:   {Pending},
}

func (s Status) String() string {
	if s < Pending || s > Failed {
		return "Unknown"
	}
	return [...]string{"pending", "indexing", "indexed", "failed"}[s]
}

// IsValid reports whether s is a recognised Status.
func (s Status) IsValid() bool {
	_, ok := allowedTransitions[s]
	return ok
}

// StatusValues returns all valid Status values.
func StatusValues() []Status {
	return []Status{Pending, Indexing, Indexed, Failed}
}

// StatusValueOf parses a string into a Status.
// Returns ErrInvalidStatus if the value is not recognised.
func StatusValueOf(s string) (Status, error) {
	switch strings.ToLower(s) {
	case "pending":
		return Pending, nil
	case "indexing":
		return Indexing, nil
	case "indexed":
		return Indexed, nil
	case "failed":
		return Failed, nil
	default:
		return 0, ErrInvalidStatus

	}
}

// TransitionTo returns nil if moving from s to next is an allowed edge.
// Returns ErrInvalidIndexStatus if next is unrecognised.
// Returns ErrInvalidStatusTransition if the edge does not exist.
func (s Status) TransitionTo(next Status) error {
	if !next.IsValid() {
		return ErrInvalidStatus
	}
	for _, allowed := range allowedTransitions[s] {
		if allowed == next {
			return nil
		}
	}
	return ErrInvalidStatusTransition
}

// Book represents an indexed technical book in the library.
type Book struct {
	id        string
	title     string
	author    string
	year      int
	status    Status
	tags      []string
	s3Key     string
	createdAt time.Time
	updatedAt time.Time
}

// NewBook constructs a validated Book with pending index status and no tags.
func NewBook(title, author string, year int) (Book, error) {
	if err := validateTitle(title); err != nil {
		return Book{}, err
	}
	if err := validateAuthor(author); err != nil {
		return Book{}, err
	}
	if err := validateYear(year); err != nil {
		return Book{}, err
	}

	now := time.Now().UTC()
	return Book{
		id:        uuid.NewString(),
		title:     title,
		author:    author,
		year:      year,
		status:    Pending,
		tags:      []string{},
		createdAt: now,
		updatedAt: now,
	}, nil
}

// NewBookFromDB reconstructs a Book from persisted data, skipping validation.
// Only repository implementations should call this.
func NewBookFromDB(
	id, title, author string,
	year int,
	status Status,
	tags []string,
	s3Key string,
	createdAt, updatedAt time.Time,
) Book {
	if tags == nil {
		tags = []string{}
	}
	return Book{
		id:        id,
		title:     title,
		author:    author,
		year:      year,
		status:    status,
		tags:      tags,
		s3Key:     s3Key,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}
}

// Id returns the book's unique identifier.
func (b *Book) Id() string { return b.id }

// Title returns the book's title.
func (b *Book) Title() string { return b.title }

// Author returns the book's author name.
func (b *Book) Author() string { return b.author }

// Year returns the book's publication year.
func (b *Book) Year() int { return b.year }

// Status returns the current ingestion pipeline state.
func (b *Book) Status() Status { return b.status }

// Tags returns the book's classification tags. Never nil.
func (b *Book) Tags() []string { return b.tags }

// S3Key returns the object storage key for the source PDF.
func (b *Book) S3Key() string { return b.s3Key }

// CreatedAt returns when the book was added to the library.
func (b *Book) CreatedAt() time.Time { return b.createdAt }

// UpdatedAt returns when the book record was last modified.
func (b *Book) UpdatedAt() time.Time { return b.updatedAt }

// SetTitle updates the title, returning an error if invalid.
func (b *Book) SetTitle(title string) error {
	if err := validateTitle(title); err != nil {
		return err
	}
	b.title = title
	b.updatedAt = time.Now().UTC()
	return nil
}

// SetAuthor updates the author, returning an error if invalid.
func (b *Book) SetAuthor(author string) error {
	if err := validateAuthor(author); err != nil {
		return err
	}
	b.author = author
	b.updatedAt = time.Now().UTC()
	return nil
}

// SetYear updates the publication year, returning an error if invalid.
func (b *Book) SetYear(year int) error {
	if err := validateYear(year); err != nil {
		return err
	}
	b.year = year
	b.updatedAt = time.Now().UTC()
	return nil
}

// SetStatus advances the pipeline state, enforcing allowed transitions.
// Does not mutate if the transition is invalid.
func (b *Book) SetStatus(next Status) error {
	if err := b.status.TransitionTo(next); err != nil {
		return err
	}
	b.status = next
	b.updatedAt = time.Now().UTC()
	return nil
}

// SetS3Key stores the object storage key for the uploaded PDF.
func (b *Book) SetS3Key(key string) error {
	if strings.TrimSpace(key) == "" {
		return ErrEmptyS3Key
	}
	b.s3Key = key
	b.updatedAt = time.Now().UTC()
	return nil
}

// SetTags replaces all classification tags. nil is treated as an empty set.
// Returns ErrInvalidTag if any tag is blank or a duplicate exists.
func (b *Book) SetTags(tags []string) error {
	if tags == nil {
		b.tags = []string{}
		return nil
	}
	if err := validateTags(tags); err != nil {
		return err
	}
	// Copy to prevent external mutation of the internal slice.
	copied := make([]string, len(tags))
	copy(copied, tags)
	b.tags = copied
	b.updatedAt = time.Now().UTC()
	return nil
}
