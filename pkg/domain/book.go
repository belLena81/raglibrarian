package domain

import (
	"time"

	"github.com/google/uuid"
)

// Book represents an indexed technical book in the library.
// All fields are private — use NewBook to construct a valid instance.
type Book struct {
	id        string
	title     string
	author    string
	year      int
	createdAt time.Time
	updatedAt time.Time
}

// NewBook creates a Book, returning an error if any field is invalid.
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

func (b *Book) Id() string           { return b.id }
func (b *Book) Title() string        { return b.title }
func (b *Book) Author() string       { return b.author }
func (b *Book) Year() int            { return b.year }
func (b *Book) CreatedAt() time.Time { return b.createdAt }
func (b *Book) UpdatedAt() time.Time { return b.updatedAt }

func (b *Book) SetTitle(title string) error {
	if err := validateTitle(title); err != nil {
		return err
	}
	b.title = title
	b.updatedAt = time.Now().UTC()
	return nil
}

func (b *Book) SetAuthor(author string) error {
	if err := validateAuthor(author); err != nil {
		return err
	}
	b.author = author
	b.updatedAt = time.Now().UTC()
	return nil
}

func (b *Book) SetYear(year int) error {
	if err := validateYear(year); err != nil {
		return err
	}
	b.year = year
	b.updatedAt = time.Now().UTC()
	return nil
}
