package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

func TestNewChunk_Valid(t *testing.T) {
	chunk, err := domain.NewChunk("book-id-123", "Goroutines are lightweight threads...", 10, 12)

	require.NoError(t, err)
	assert.NotEmpty(t, chunk.Id())
	assert.Equal(t, "book-id-123", chunk.BookId())
	assert.Equal(t, "Goroutines are lightweight threads...", chunk.Content())
	assert.Equal(t, 10, chunk.PageStart())
	assert.Equal(t, 12, chunk.PageEnd())
	assert.WithinDuration(t, time.Now().UTC(), chunk.CreatedAt(), time.Second)
}

func TestNewChunk_UniqueIDs(t *testing.T) {
	a, err := domain.NewChunk("book-id-123", "Some content", 1, 1)
	require.NoError(t, err)

	b, err := domain.NewChunk("book-id-123", "Some content", 1, 1)
	require.NoError(t, err)

	assert.NotEqual(t, a.Id(), b.Id())
}

func TestNewChunk_InvalidBookID(t *testing.T) {
	tests := []struct {
		name   string
		bookID string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewChunk(tt.bookID, "Some content", 1, 1)
			assert.ErrorIs(t, err, domain.ErrEmptyBookId)
		})
	}
}

func TestNewChunk_InvalidContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewChunk("book-id-123", tt.content, 1, 1)
			assert.ErrorIs(t, err, domain.ErrEmptyContent)
		})
	}
}

func TestNewChunk_InvalidPageRange(t *testing.T) {
	tests := []struct {
		name      string
		pageStart int
		pageEnd   int
	}{
		{"zero start", 0, 1},
		{"negative start", -1, 1},
		{"end before start", 5, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewChunk("book-id-123", "Some content", tt.pageStart, tt.pageEnd)
			assert.ErrorIs(t, err, domain.ErrInvalidPages)
		})
	}
}

func TestNewChunk_SinglePage(t *testing.T) {
	chunk, err := domain.NewChunk("book-id-123", "Some content", 5, 5)
	require.NoError(t, err)
	assert.Equal(t, 5, chunk.PageStart())
	assert.Equal(t, 5, chunk.PageEnd())
}
