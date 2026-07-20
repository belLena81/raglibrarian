package application

import (
	"context"
	"errors"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

var ErrSearchForbidden = errors.New("search forbidden")

// Evidence is Retrieval's controlled local projection returned to an authorized caller.
type Evidence struct {
	EvidenceID, ChunkID, JobID, BookID, Title, Author, Chapter, Section, Passage string
	Year                                                                         int
	Tags                                                                         []string
	PageStart, PageEnd                                                           uint32
	Score                                                                        float64
}

type QueryEmbedder interface {
	EmbedQuery(context.Context, string) ([]float32, error)
}

type EvidenceStore interface {
	Search(context.Context, domain.SearchQuery, []float32) ([]Evidence, error)
}

type IndexVisibility interface {
	FilterIndexed(context.Context, []Evidence) ([]Evidence, error)
}

type Searcher struct {
	embedder   QueryEmbedder
	store      EvidenceStore
	visibility IndexVisibility
}

func NewSearcher(embedder QueryEmbedder, store EvidenceStore, visibility IndexVisibility) (*Searcher, error) {
	if embedder == nil || store == nil || visibility == nil {
		return nil, errors.New("invalid searcher configuration")
	}
	return &Searcher{embedder: embedder, store: store, visibility: visibility}, nil
}

func (s *Searcher) Search(ctx context.Context, actor domain.Actor, input domain.SearchQueryInput) ([]Evidence, error) {
	if !actor.CanSearch() {
		return nil, ErrSearchForbidden
	}
	query, err := domain.NewSearchQuery(input)
	if err != nil {
		return nil, err
	}
	vector, err := s.embedder.EmbedQuery(ctx, query.Question())
	if err != nil {
		return nil, errors.New("embed query")
	}
	if len(vector) != domain.EmbeddingDimensions {
		return nil, errors.New("invalid embedding dimensions")
	}
	results, err := s.store.Search(ctx, query, vector)
	if err != nil {
		return nil, errors.New("search evidence")
	}
	results, err = s.visibility.FilterIndexed(ctx, results)
	if err != nil {
		return nil, errors.New("validate index visibility")
	}
	return results, nil
}
