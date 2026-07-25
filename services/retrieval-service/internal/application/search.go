package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/errgroup"

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
	embedder        QueryEmbedder
	store           EvidenceStore
	visibility      IndexVisibility
	summaryProvider SummaryProvider
}

func NewSearcher(embedder QueryEmbedder, store EvidenceStore, visibility IndexVisibility) (*Searcher, error) {
	if embedder == nil || store == nil || visibility == nil {
		return nil, errors.New("invalid searcher configuration")
	}
	return &Searcher{embedder: embedder, store: store, visibility: visibility}, nil
}

func (s *Searcher) SetSummaryProvider(provider SummaryProvider) {
	s.summaryProvider = provider
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
	s.populateEvidenceSummaries(ctx, results)
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
	s.populateDocumentSummaries(ctx, results)
	return results, nil
}

func (s *Searcher) populateEvidenceSummaries(ctx context.Context, results []Evidence) {
	if len(results) == 0 {
		return
	}
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for index := range results {
		index := index
		group.Go(func() error {
			results[index].Summary = s.summarizeEvidence(groupContext, results[index])
			return nil
		})
	}
	_ = group.Wait()
}

func (s *Searcher) populateDocumentSummaries(ctx context.Context, results []DocumentResult) {
	if len(results) == 0 {
		return
	}
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for index := range results {
		index := index
		group.Go(func() error {
			for evidenceIndex := range results[index].Evidence {
				results[index].Evidence[evidenceIndex].Summary = s.summarizeEvidence(groupContext, results[index].Evidence[evidenceIndex])
			}
			results[index].Summary = s.summarizeDocument(groupContext, results[index])
			return nil
		})
	}
	_ = group.Wait()
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

func (s *Searcher) summarizeEvidence(ctx context.Context, value Evidence) string {
	return s.summarizeText(ctx, value.Passage)
}

func (s *Searcher) summarizeDocument(ctx context.Context, value DocumentResult) string {
	input := normalizeDocumentSummaryInput(value)
	if input == "" {
		return ""
	}
	return s.summarizeText(ctx, input)
}

func (s *Searcher) summarizeText(ctx context.Context, value string) string {
	normalized := normalizeSummaryInput(value)
	if normalized == "" {
		return ""
	}
	if s.summaryProvider != nil {
		summaryContext, cancel := context.WithTimeout(ctx, summaryProviderTimeout)
		defer cancel()
		summary, err := s.summaryProvider.Summarize(summaryContext, normalized)
		if err == nil {
			if sanitized := normalizeProviderSummary(summary); sanitized != "" {
				return sanitized
			}
		}
	}
	return summarizeText(normalized)
}

func normalizeSummaryInput(value string) string {
	normalized := strings.Join(strings.Fields(value), " ")
	if normalized == "" {
		return ""
	}
	const maximumSummaryInputRunes = 4096
	if utf8.RuneCountInString(normalized) <= maximumSummaryInputRunes {
		return normalized
	}
	runes := []rune(normalized)
	return strings.TrimSpace(string(runes[:maximumSummaryInputRunes]))
}

func normalizeProviderSummary(value string) string {
	normalized := strings.Join(strings.Fields(value), " ")
	if normalized == "" {
		return ""
	}
	const maximumSummaryRunes = 220
	if utf8.RuneCountInString(normalized) <= maximumSummaryRunes {
		return normalized
	}
	runes := []rune(normalized)
	return strings.TrimSpace(string(runes[:maximumSummaryRunes-1])) + "…"
}

func normalizeDocumentSummaryInput(value DocumentResult) string {
	parts := make([]string, 0, 2)
	for _, evidence := range value.Evidence {
		part := normalizeSummaryInput(evidence.Passage)
		if part == "" {
			continue
		}
		parts = append(parts, part)
		if len(parts) >= 2 {
			break
		}
	}
	return strings.Join(parts, " ")
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

type SummaryProvider interface {
	Summarize(context.Context, string) (string, error)
}

const summaryProviderTimeout = 2 * time.Second
