package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

func TestNewBook_Valid(t *testing.T) {
	book, err := domain.NewBook("The Go Programming Language", "Donovan & Kernighan", 2015)

	require.NoError(t, err)
	assert.NotEmpty(t, book.Id())
	assert.Equal(t, "The Go Programming Language", book.Title())
	assert.Equal(t, "Donovan & Kernighan", book.Author())
	assert.Equal(t, 2015, book.Year())
	assert.WithinDuration(t, time.Now().UTC(), book.CreatedAt(), time.Second)
	assert.WithinDuration(t, time.Now().UTC(), book.UpdatedAt(), time.Second)
}

func TestNewBook_UniqueIDs(t *testing.T) {
	a, err := domain.NewBook("Clean Code", "Robert Martin", 2008)
	require.NoError(t, err)

	b, err := domain.NewBook("Clean Code", "Robert Martin", 2008)
	require.NoError(t, err)

	assert.NotEqual(t, a.Id(), b.Id())
}

func TestNewBook_InvalidTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"tab only", "\t"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewBook(tt.title, "Some Author", 2020)
			assert.ErrorIs(t, err, domain.ErrEmptyTitle)
		})
	}
}

func TestNewBook_InvalidAuthor(t *testing.T) {
	tests := []struct {
		name   string
		author string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewBook("Valid Title", tt.author, 2020)
			assert.ErrorIs(t, err, domain.ErrEmptyAuthor)
		})
	}
}

func TestNewBook_InvalidYear(t *testing.T) {
	currentYear := time.Now().UTC().Year()

	tests := []struct {
		name string
		year int
	}{
		{"zero", 0},
		{"negative", -1},
		{"too old", 1899},
		{"future", currentYear + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewBook("Valid Title", "Valid Author", tt.year)
			assert.ErrorIs(t, err, domain.ErrInvalidYear)
		})
	}
}

func TestBook_SetTitle(t *testing.T) {
	book, err := domain.NewBook("Original Title", "Author", 2020)
	require.NoError(t, err)

	updatedBefore := book.UpdatedAt()
	time.Sleep(time.Millisecond) // ensure updatedAt advances

	err = book.SetTitle("New Title")
	require.NoError(t, err)
	assert.Equal(t, "New Title", book.Title())
	assert.True(t, book.UpdatedAt().After(updatedBefore))
}

func TestBook_SetTitle_Invalid(t *testing.T) {
	book, err := domain.NewBook("Original Title", "Author", 2020)
	require.NoError(t, err)

	err = book.SetTitle("")
	assert.ErrorIs(t, err, domain.ErrEmptyTitle)
	assert.Equal(t, "Original Title", book.Title()) // unchanged
}

func TestBook_SetAuthor(t *testing.T) {
	book, err := domain.NewBook("Title", "Original Author", 2020)
	require.NoError(t, err)

	err = book.SetAuthor("New Author")
	require.NoError(t, err)
	assert.Equal(t, "New Author", book.Author())
}

func TestBook_SetAuthor_Invalid(t *testing.T) {
	book, err := domain.NewBook("Title", "Original Author", 2020)
	require.NoError(t, err)

	err = book.SetAuthor("   ")
	assert.ErrorIs(t, err, domain.ErrEmptyAuthor)
	assert.Equal(t, "Original Author", book.Author()) // unchanged
}

func TestBook_SetYear(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)

	err = book.SetYear(2021)
	require.NoError(t, err)
	assert.Equal(t, 2021, book.Year())
}

func TestBook_SetYear_Invalid(t *testing.T) {
	book, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)

	err = book.SetYear(1800)
	assert.ErrorIs(t, err, domain.ErrInvalidYear)
	assert.Equal(t, 2020, book.Year()) // unchanged
}
