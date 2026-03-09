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
// Plain struct instead of testify/mock — simpler and shows intent clearly.

type fakeQueryRepository struct {
	results []domain.SearchResult
	err     error
}

func (f *fakeQueryRepository) Search(_ context.Context, _ domain.Query) ([]domain.SearchResult, error) {
	return f.results, f.err
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// makeSearchResult builds a realistic SearchResult for use in tests.
// queryId is a placeholder — the service creates its own domain.Query internally.
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
	result := makeSearchResult(t, "placeholder-id")
	repo := &fakeQueryRepository{results: []domain.SearchResult{result}}
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
	r1 := makeSearchResult(t, "qid")
	book2, err := domain.NewBook("Book Two", "Author B", 2021)
	require.NoError(t, err)
	r2, err := domain.NewSearchResult("qid", book2, "Ch2", "passage two", []int{2}, 0.80)
	require.NoError(t, err)

	repo := &fakeQueryRepository{results: []domain.SearchResult{r1, r2}}
	svc := usecase.NewQueryService(repo)

	results, err := svc.Answer(context.Background(), "user-123", "Valid question?")

	require.NoError(t, err)
	require.Len(t, results, 2)
	gotBook1 := results[0].Book()
	gotBook2 := results[1].Book()
	assert.Equal(t, "The Go Programming Language", gotBook1.Title())
	assert.Equal(t, "Book Two", gotBook2.Title())
}

func TestNewQueryService_NilRepository_Panics(t *testing.T) {
	assert.Panics(t, func() {
		usecase.NewQueryService(nil)
	})
}
