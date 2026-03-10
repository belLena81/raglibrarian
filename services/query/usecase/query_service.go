// Package usecase contains the application layer for the query service.
package usecase

import (
	"context"
	"fmt"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/query/repository"
)

// QueryUseCase is the application-layer contract for answering user questions.
type QueryUseCase interface {
	// Answer executes a semantic search for the given natural-language question.
	Answer(ctx context.Context, userId, question string) ([]domain.SearchResult, error)
}

// QueryService is the production implementation of QueryUseCase.
type QueryService struct {
	repo repository.QueryRepository
}

// NewQueryService constructs a QueryService. Panics if repo is nil.
func NewQueryService(repo repository.QueryRepository) *QueryService {
	if repo == nil {
		panic("usecase: QueryRepository must not be nil")
	}
	return &QueryService{repo: repo}
}

// Answer validates inputs via domain.NewQuery, then delegates to the repository.
func (s *QueryService) Answer(ctx context.Context, userId, question string) ([]domain.SearchResult, error) {
	q, err := domain.NewQuery(userId, question)
	if err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}

	results, err := s.repo.Search(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	return results, nil
}
