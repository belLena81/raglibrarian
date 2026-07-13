package repository

import (
	"context"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// StubQueryRepository satisfies QueryRepository with hard-coded results.
// Used in Iteration 1 before real infrastructure is wired.
type StubQueryRepository struct{}

// NewStubQueryRepository constructs a StubQueryRepository.
func NewStubQueryRepository() *StubQueryRepository {
	return &StubQueryRepository{}
}

// Search returns two deterministic SearchResults regardless of the question.
func (r *StubQueryRepository) Search(_ context.Context, q domain.Query) ([]domain.SearchResult, error) {
	goBook, _ := domain.NewBook("The Go Programming Language", "Donovan & Kernighan", 2015)
	cleanBook, _ := domain.NewBook("Clean Code", "Robert C. Martin", 2008)

	first, _ := domain.NewSearchResult(
		q.Id(),
		goBook,
		"Chapter 9 — Concurrency",
		"Goroutines are multiplexed onto a small number of OS threads "+
			"by the Go scheduler using an M:N threading model.",
		[]int{217, 218, 219},
		0.94,
	)

	second, _ := domain.NewSearchResult(
		q.Id(),
		cleanBook,
		"Chapter 13 — Concurrency",
		"Keep the concurrency-related code separate from other code. "+
			"It helps to reason about both in isolation.",
		[]int{182, 183},
		0.81,
	)

	return []domain.SearchResult{first, second}, nil
}
