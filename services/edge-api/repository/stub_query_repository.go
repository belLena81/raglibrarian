package repository

import (
	"context"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// StubQueryRepository makes the M1 retrieval limitation explicit. It must not
// return example passages because citations are only valid when backed by
// retrieved evidence.
type StubQueryRepository struct{}

// NewStubQueryRepository constructs a StubQueryRepository.
func NewStubQueryRepository() *StubQueryRepository {
	return &StubQueryRepository{}
}

// Search reports that retrieval has not yet been implemented.
func (r *StubQueryRepository) Search(_ context.Context, _ domain.Query) ([]domain.SearchResult, error) {
	return nil, domain.ErrRetrievalUnavailable
}
