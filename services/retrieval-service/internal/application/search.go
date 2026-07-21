package application

import (
	"context"
	"errors"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

var ErrSearchForbidden = errors.New("search forbidden")

const maximumSearchCandidates = domain.MaximumResultLimit * 5

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
	Search(context.Context, domain.SearchQuery, []float32, int, int) ([]Evidence, error)
	SearchDocuments(context.Context, domain.SearchQuery, []float32, int, int) ([]DocumentResult, error)
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
	results, err := s.searchVisibleEvidence(ctx, query, vector)
	if err != nil {
		return SearchResult{}, err
	}
	documents, err := s.searchVisibleDocuments(ctx, query, vector)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Evidence: results, Documents: documents}, nil
}

func (s *Searcher) searchVisibleEvidence(ctx context.Context, query domain.SearchQuery, vector []float32) ([]Evidence, error) {
	results := make([]Evidence, 0, query.Limit())
	for offset, pageLimit := 0, searchPageLimit(query.Limit()); len(results) < query.Limit() && offset < maximumSearchCandidates; offset += pageLimit {
		candidateLimit := searchCandidateLimit(pageLimit, offset)
		candidates, err := s.store.Search(ctx, query, vector, candidateLimit, offset)
		if err != nil {
			return nil, errors.New("search evidence")
		}
		visible, err := s.visibility.FilterIndexed(ctx, candidates)
		if err != nil {
			return nil, errors.New("validate index visibility")
		}
		results = append(results, visible...)
		if len(candidates) < candidateLimit {
			break
		}
	}
	return trimEvidence(results, query.Limit()), nil
}

func (s *Searcher) searchVisibleDocuments(ctx context.Context, query domain.SearchQuery, vector []float32) ([]DocumentResult, error) {
	results := make([]DocumentResult, 0, query.Limit())
	for offset, pageLimit := 0, searchPageLimit(query.Limit()); len(results) < query.Limit() && offset < maximumSearchCandidates; offset += pageLimit {
		candidateLimit := searchCandidateLimit(pageLimit, offset)
		candidates, err := s.store.SearchDocuments(ctx, query, vector, candidateLimit, offset)
		if err != nil {
			return nil, errors.New("search documents")
		}
		visible, err := s.visibility.FilterIndexedDocuments(ctx, candidates)
		if err != nil {
			return nil, errors.New("validate document visibility")
		}
		results = append(results, visible...)
		if len(candidates) < candidateLimit {
			break
		}
	}
	return trimDocuments(results, query.Limit()), nil
}

func searchPageLimit(limit int) int {
	pageLimit := limit * 2
	if pageLimit > maximumSearchCandidates {
		return maximumSearchCandidates
	}
	return pageLimit
}

func searchCandidateLimit(pageLimit, offset int) int {
	if offset+pageLimit > maximumSearchCandidates {
		return maximumSearchCandidates - offset
	}
	return pageLimit
}

func trimEvidence(values []Evidence, limit int) []Evidence {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

func trimDocuments(values []DocumentResult, limit int) []DocumentResult {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}
