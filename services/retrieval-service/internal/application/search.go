package application

import (
	"context"
	"errors"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

var ErrSearchForbidden = errors.New("search forbidden")

// Evidence is Retrieval's controlled local chunk projection returned to an authorized caller.
type Evidence struct {
	EvidenceID, ChunkID, JobID, BookID, Title, Author, Chapter, Section, Passage string
	Year                                                                         int
	Tags                                                                         []string
	PageStart, PageEnd                                                           uint32
	Score                                                                        float64
}

// DocumentResult is Retrieval's controlled document-level projection with stored chunk evidence.
type DocumentResult struct {
	DocumentID, JobID, BookID, Title, Author string
	Year                                     int
	Tags                                     []string
	ChunkCount                               uint32
	PageStart, PageEnd                       uint32
	Score                                    float64
	Evidence                                 []Evidence
}

// SearchResult contains Retrieval-owned search projections.
type SearchResult struct {
	Evidence  []Evidence
	Documents []DocumentResult
}

type QueryEmbedder interface {
	EmbedQuery(context.Context, string) ([]float32, error)
}

type EvidenceStore interface {
	Search(context.Context, domain.SearchQuery, []float32) ([]Evidence, error)
	SearchDocuments(context.Context, domain.SearchQuery, []float32) ([]DocumentResult, error)
}

type IndexVisibility interface {
	FilterIndexed(context.Context, []Evidence) ([]Evidence, error)
	FilterIndexedDocuments(context.Context, []DocumentResult) ([]DocumentResult, error)
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

func (s *Searcher) Search(ctx context.Context, actor domain.Actor, input domain.SearchQueryInput) (SearchResult, error) {
	if !actor.CanSearch() {
		return SearchResult{}, ErrSearchForbidden
	}
	query, err := domain.NewSearchQuery(input)
	if err != nil {
		return SearchResult{}, err
	}
	vector, err := s.embedder.EmbedQuery(ctx, query.Question())
	if err != nil {
		return SearchResult{}, errors.New("embed query")
	}
	if len(vector) != domain.EmbeddingDimensions {
		return SearchResult{}, errors.New("invalid embedding dimensions")
	}
	results, err := s.store.Search(ctx, query, vector)
	if err != nil {
		return SearchResult{}, errors.New("search evidence")
	}
	results, err = s.visibility.FilterIndexed(ctx, results)
	if err != nil {
		return SearchResult{}, errors.New("validate index visibility")
	}
	documents, err := s.store.SearchDocuments(ctx, query, vector)
	if err != nil {
		return SearchResult{}, errors.New("search documents")
	}
	documents, err = s.visibility.FilterIndexedDocuments(ctx, documents)
	if err != nil {
		return SearchResult{}, errors.New("validate document visibility")
	}
	return SearchResult{Evidence: results, Documents: documents}, nil
}
