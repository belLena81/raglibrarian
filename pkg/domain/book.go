// Package domain contains the core business entities and rules for raglibrarian.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Book represents an indexed technical book in the library.
type Book struct {
	id        string
	title     string
	author    string
	year      int
	createdAt time.Time
	updatedAt time.Time
}

// NewBook constructs a validated Book.
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
		createdAt: now,
		updatedAt: now,
	}, nil
}

// NewBookFromDb reconstructs a Book from persisted data, skipping validation.
// Only repository implementations should call this.
func NewBookFromDb(id, title, author string, year int, createdAt, updatedAt time.Time) Book {
	return Book{
		id:        id,
		title:     title,
		author:    author,
		year:      year,
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
