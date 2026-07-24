package application

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

var ErrSearchForbidden = errors.New("search forbidden")

const maximumSearchCandidates = domain.MaximumResultLimit * 5

// Evidence is Retrieval's controlled local chunk projection returned to an authorized caller.
type Evidence struct {
	EvidenceID, ChunkID, JobID, BookID, Title, Author, MediaType, Chapter, Section, Passage string
	Year                                                                                    int
	Tags                                                                                    []string
	PageStart, PageEnd                                                                      uint32
	Score                                                                                   float64
	Summary                                                                                 string
}

// DocumentResult is Retrieval's controlled document-level projection with stored chunk evidence.
type DocumentResult struct {
	DocumentID, JobID, BookID, Title, Author, MediaType string
	Year                                                int
	Tags                                                []string
	ChunkCount                                          uint32
	PageStart, PageEnd                                  uint32
	Score                                               float64
	Evidence                                            []Evidence
	Summary                                             string
}

// DocumentPage is a hydrated document-search page. Exhausted reflects the raw
// Qdrant page, before candidates without usable chunk evidence are omitted.
type DocumentPage struct {
	Documents []DocumentResult
	Exhausted bool
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
	SearchDocuments(context.Context, domain.SearchQuery, []float32, int, int) (DocumentPage, error)
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
	results = trimEvidence(results, query.Limit())
	for index := range results {
		results[index].Summary = summarizeEvidence(results[index])
	}
	return results, nil
}

func (s *Searcher) searchVisibleDocuments(ctx context.Context, query domain.SearchQuery, vector []float32) ([]DocumentResult, error) {
	results := make([]DocumentResult, 0, query.Limit())
	for offset, pageLimit := 0, searchPageLimit(query.Limit()); len(results) < query.Limit() && offset < maximumSearchCandidates; offset += pageLimit {
		candidateLimit := searchCandidateLimit(pageLimit, offset)
		page, err := s.store.SearchDocuments(ctx, query, vector, candidateLimit, offset)
		if err != nil {
			return nil, errors.New("search documents")
		}
		visible, err := s.visibility.FilterIndexedDocuments(ctx, page.Documents)
		if err != nil {
			return nil, errors.New("validate document visibility")
		}
		results = append(results, visible...)
		if page.Exhausted {
			break
		}
	}
	results = trimDocuments(results, query.Limit())
	for index := range results {
		for evidenceIndex := range results[index].Evidence {
			results[index].Evidence[evidenceIndex].Summary = summarizeEvidence(results[index].Evidence[evidenceIndex])
		}
		results[index].Summary = summarizeDocument(results[index])
	}
	return results, nil
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

func summarizeEvidence(value Evidence) string {
	return summarizeText(value.Passage)
}

func summarizeDocument(value DocumentResult) string {
	if len(value.Evidence) == 0 {
		return ""
	}
	parts := make([]string, 0, len(value.Evidence))
	for _, evidence := range value.Evidence {
		summary := summarizeText(evidence.Passage)
		if summary != "" {
			parts = append(parts, summary)
		}
		if len(parts) >= 2 {
			break
		}
	}
	return summarizeText(strings.Join(parts, " "))
}

func summarizeText(value string) string {
	normalized := strings.Join(strings.Fields(value), " ")
	if normalized == "" {
		return ""
	}
	firstSentence := normalized
	if match := firstSentenceMatch(normalized); match != "" {
		firstSentence = match
	}
	const maximumSummaryRunes = 220
	if utf8.RuneCountInString(firstSentence) <= maximumSummaryRunes {
		return firstSentence
	}
	runes := []rune(firstSentence)
	return strings.TrimSpace(string(runes[:maximumSummaryRunes-1])) + "…"
}

func firstSentenceMatch(value string) string {
	for index, r := range value {
		if r == '.' || r == '!' || r == '?' {
			if index+1 == len(value) || value[index+1] == ' ' {
				return value[:index+1]
			}
		}
	}
	return ""
}
