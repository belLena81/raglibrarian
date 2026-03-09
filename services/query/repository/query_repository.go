// Package repository defines the port (interface) the query use case depends on.
// Concrete adapters (stub, postgres, etc.) implement this interface; the domain
// layer never imports them.
package repository

import (
	"context"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// QueryRepository is the read-side port for the query use case.
// Any adapter — in-memory stub, Qdrant, Postgres — must satisfy this contract.
type QueryRepository interface {
	// Search returns a ranked list of SearchResults for the given Query.
	// It must return an empty slice (not nil) when no results are found.
	Search(ctx context.Context, q domain.Query) ([]domain.SearchResult, error)
}
