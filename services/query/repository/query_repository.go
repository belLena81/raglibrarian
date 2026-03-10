// Package repository defines the read-side port for the query use case.
package repository

import (
	"context"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// QueryRepository is the read-side port for the query use case.
type QueryRepository interface {
	// Search returns ranked SearchResults for the given Query.
	// Must return an empty slice (not nil) when no results are found.
	Search(ctx context.Context, q domain.Query) ([]domain.SearchResult, error)
}
