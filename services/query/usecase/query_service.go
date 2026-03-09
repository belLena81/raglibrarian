// Package usecase contains the application layer for the query service.
// It orchestrates domain objects and depends only on the repository port —
// never on HTTP, gRPC, or any infrastructure concern.
package usecase

import (
	"context"
	"fmt"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/query/repository"
)

// QueryUseCase is the application-layer contract for answering user questions.
// Defining it as an interface here lets the HTTP handler depend on an
// abstraction, not the concrete service — making the handler trivially
// testable with a mock.
type QueryUseCase interface {
	// Answer executes a semantic search for the user's natural-language question.
	// userId and question are validated inside the domain constructor — the use
	// case never does raw string checks itself.
	Answer(ctx context.Context, userId, question string) ([]domain.SearchResult, error)
}

// QueryService is the production implementation of QueryUseCase.
// It delegates retrieval to the injected QueryRepository, keeping all
// infrastructure decisions outside this file.
type QueryService struct {
	repo repository.QueryRepository
}

// NewQueryService constructs a QueryService.  Panics if repo is nil to catch
// misconfigured wiring at startup rather than at request time.
func NewQueryService(repo repository.QueryRepository) *QueryService {
	if repo == nil {
		panic("usecase: QueryRepository must not be nil")
	}
	return &QueryService{repo: repo}
}

// Answer validates the inputs by constructing a domain.Query, then delegates
// to the repository for retrieval.  Domain errors (ErrEmptyQuestion, etc.) are
// surfaced directly to the caller.
func (s *QueryService) Answer(ctx context.Context, userId, question string) ([]domain.SearchResult, error) {
	q, err := domain.NewQuery(userId, question)
	if err != nil {
		// domain validation error — caller decides the HTTP status code
		return nil, fmt.Errorf("invalid query: %w", err)
	}

	results, err := s.repo.Search(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	return results, nil
}
