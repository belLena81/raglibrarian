// Package repository contains adapters that satisfy the QueryRepository port.
// StubQueryRepository is the Iteration 1 implementation — it returns
// hard-coded, realistic results so the API contract can be verified and
// consumed by the UI before any real infrastructure exists.
package repository

import (
	"context"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// StubQueryRepository satisfies QueryRepository with deterministic fake data.
// It lives in the infra/stub layer; no domain logic lives here.
type StubQueryRepository struct{}

// NewStubQueryRepository constructs a StubQueryRepository.
func NewStubQueryRepository() *StubQueryRepository {
	return &StubQueryRepository{}
}

// Search returns two hard-coded SearchResults regardless of the question,
// proving the response shape before any vector search is wired.
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
