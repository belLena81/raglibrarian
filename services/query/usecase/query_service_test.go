package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/query/usecase"
)

// ── Fake Repository ──────────────────────────────────────────────────────────
// A minimal in-process fake satisfying QueryRepository.
// It does NOT use testify/mock — a plain struct is simpler, faster, and shows
// intent more clearly for a single-method interface.

type fakeQueryRepository struct {
	results []domain.SearchResult
	err     error
}

func (f *fakeQueryRepository) Search(_ context.Context, _ domain.Query) ([]domain.SearchResult, error) {
	return f.results, f.err
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func makeSearchResult(t *testing.T, queryId string) domain.SearchResult {
	t.Helper()
	book, err := domain.NewBook("The Go Programming Language", "Donovan & Kernighan", 2015)
	require.NoError(t, err)
	result, err := domain.NewSearchResult(
		queryId,
		book,
		"Chapter 9 — Concurrency",
		"Goroutines are multiplexed onto OS threads...",
		[]int{217, 218},
		0.94,
	)
	require.NoError(t, err)
	return result
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestQueryService_Answer_ReturnsResults(t *testing.T) {
	// We build the result with a placeholder query id; the service creates its
	// own domain.Query internally, so we just verify the slice comes back.
	book, _ := domain.NewBook("The Go Programming Language", "Donovan & Kernighan", 2015)
	placeholder, _ := domain.NewSearchResult(
		"placeholder-id",
		book,
		"Chapter 9",
		"Some passage",
		[]int{1},
		0.9,
	)

	repo := &fakeQueryRepository{results: []domain.SearchResult{placeholder}}
	svc := usecase.NewQueryService(repo)

	results, err := svc.Answer(context.Background(), "user-123", "What is a goroutine?")

	require.NoError(t, err)
	assert.Len(t, results, 1)
}

func TestQueryService_Answer_EmptyQuestion_ReturnsDomainError(t *testing.T) {
	repo := &fakeQueryRepository{}
	svc := usecase.NewQueryService(repo)

	_, err := svc.Answer(context.Background(), "user-123", "")

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrEmptyQuestion)
}

func TestQueryService_Answer_WhitespaceQuestion_ReturnsDomainError(t *testing.T) {
	repo := &fakeQueryRepository{}
	svc := usecase.NewQueryService(repo)

	_, err := svc.Answer(context.Background(), "user-123", "   ")

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrEmptyQuestion)
}

func TestQueryService_Answer_EmptyUserId_ReturnsDomainError(t *testing.T) {
	repo := &fakeQueryRepository{}
	svc := usecase.NewQueryService(repo)

	_, err := svc.Answer(context.Background(), "", "Valid question?")

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrEmptyUserId)
}

func TestQueryService_Answer_WhitespaceUserId_ReturnsDomainError(t *testing.T) {
	repo := &fakeQueryRepository{}
	svc := usecase.NewQueryService(repo)

	_, err := svc.Answer(context.Background(), "   ", "Valid question?")

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrEmptyUserId)
}

func TestQueryService_Answer_RepositoryError_Propagates(t *testing.T) {
	repoErr := errors.New("qdrant: connection refused")
	repo := &fakeQueryRepository{err: repoErr}
	svc := usecase.NewQueryService(repo)

	_, err := svc.Answer(context.Background(), "user-123", "Valid question?")

	require.Error(t, err)
	assert.ErrorIs(t, err, repoErr)
}

func TestQueryService_Answer_EmptyResults_ReturnsEmptySlice(t *testing.T) {
	repo := &fakeQueryRepository{results: []domain.SearchResult{}}
	svc := usecase.NewQueryService(repo)

	results, err := svc.Answer(context.Background(), "user-123", "What is a goroutine?")

	require.NoError(t, err)
	assert.Empty(t, results)
	assert.NotNil(t, results) // must be empty slice, not nil
}

func TestQueryService_Answer_MultipleResults_PreservesOrder(t *testing.T) {
	book1, _ := domain.NewBook("Book One", "Author A", 2020)
	book2, _ := domain.NewBook("Book Two", "Author B", 2021)

	r1, _ := domain.NewSearchResult("qid", book1, "Ch1", "passage one", []int{1}, 0.95)
	r2, _ := domain.NewSearchResult("qid", book2, "Ch2", "passage two", []int{2}, 0.80)

	repo := &fakeQueryRepository{results: []domain.SearchResult{r1, r2}}
	svc := usecase.NewQueryService(repo)

	results, err := svc.Answer(context.Background(), "user-123", "Valid question?")

	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "Book One", results[0].Book().Title())
	assert.Equal(t, "Book Two", results[1].Book().Title())
}

func TestNewQueryService_NilRepository_Panics(t *testing.T) {
	assert.Panics(t, func() {
		usecase.NewQueryService(nil)
	})
}
