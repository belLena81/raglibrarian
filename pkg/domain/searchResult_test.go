package domain_test

import (
	"testing"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validBook(t *testing.T) domain.Book {
	t.Helper()
	book, err := domain.NewBook("The Go Programming Language", "Donovan & Kernighan", 2015)
	require.NoError(t, err)
	return book
}

func TestNewSearchResult_Valid(t *testing.T) {
	book := validBook(t)

	result, err := domain.NewSearchResult(
		"query-id-789",
		book,
		"Chapter 9 — Concurrency",
		"Goroutines are multiplexed onto OS threads...",
		[]int{217, 218, 219},
		0.94,
	)

	gotBook := result.Book()

	require.NoError(t, err)
	assert.NotEmpty(t, result.Id())
	assert.Equal(t, "query-id-789", result.QueryId())
	assert.Equal(t, book.Id(), gotBook.Id())
	assert.Equal(t, "Chapter 9 — Concurrency", result.Chapter())
	assert.Equal(t, "Goroutines are multiplexed onto OS threads...", result.Passage())
	assert.Equal(t, []int{217, 218, 219}, result.Pages())
	assert.InDelta(t, 0.94, result.Score(), 0.0001)
}

func TestNewSearchResult_InvalidQueryId(t *testing.T) {
	book := validBook(t)

	_, err := domain.NewSearchResult("", book, "Chapter 1", "Some passage", []int{1}, 0.9)
	assert.ErrorIs(t, err, domain.ErrEmptyQueryId)
}

func TestNewSearchResult_InvalidChapter(t *testing.T) {
	book := validBook(t)

	tests := []struct {
		name    string
		chapter string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewSearchResult("query-id-789", book, tt.chapter, "Some passage", []int{1}, 0.9)
			assert.ErrorIs(t, err, domain.ErrEmptyChapter)
		})
	}
}

func TestNewSearchResult_InvalidPassage(t *testing.T) {
	book := validBook(t)

	_, err := domain.NewSearchResult("query-id-789", book, "Chapter 1", "", []int{1}, 0.9)
	assert.ErrorIs(t, err, domain.ErrEmptyPassage)
}

func TestNewSearchResult_InvalidPages(t *testing.T) {
	book := validBook(t)

	_, err := domain.NewSearchResult("query-id-789", book, "Chapter 1", "Some passage", []int{}, 0.9)
	assert.ErrorIs(t, err, domain.ErrEmptyPages)
}

func TestNewSearchResult_InvalidScore(t *testing.T) {
	book := validBook(t)

	tests := []struct {
		name  string
		score float64
	}{
		{"negative", -0.1},
		{"above one", 1.1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewSearchResult("query-id-789", book, "Chapter 1", "Some passage", []int{1}, tt.score)
			assert.ErrorIs(t, err, domain.ErrInvalidScore)
		})
	}
}

func TestNewSearchResult_BoundaryScores(t *testing.T) {
	book := validBook(t)

	for _, score := range []float64{0.0, 1.0} {
		result, err := domain.NewSearchResult("query-id-789", book, "Chapter 1", "Some passage", []int{1}, score)
		require.NoError(t, err)
		assert.InDelta(t, score, result.Score(), 0.0001)
	}
}
