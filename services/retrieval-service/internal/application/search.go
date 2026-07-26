package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
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
	embedder            QueryEmbedder
	store               EvidenceStore
	visibility          IndexVisibility
	summaryProvider     SummaryProvider
	summaryTimeout      time.Duration
	summaryCallLimit    int
	minimumVisibleScore float64
}

type SummaryRequest struct {
	Question string
	Passage  string
}

func NewSearcher(embedder QueryEmbedder, store EvidenceStore, visibility IndexVisibility, minimumVisibleScore float64, summaryCallLimit int) (*Searcher, error) {
	if embedder == nil || store == nil || visibility == nil || summaryCallLimit < 0 {
		return nil, errors.New("invalid searcher configuration")
	}
	return &Searcher{embedder: embedder, store: store, visibility: visibility, minimumVisibleScore: minimumVisibleScore, summaryCallLimit: summaryCallLimit}, nil
}

func (s *Searcher) SetSummaryProvider(provider SummaryProvider) {
	s.summaryProvider = provider
}

func (s *Searcher) SetSummaryProviderTimeout(timeout time.Duration) {
	s.summaryTimeout = timeout
}

func (s *Searcher) Search(ctx context.Context, actor domain.Actor, input domain.SearchQueryInput) (SearchResult, error) {
	if !actor.CanSearch() {
		return SearchResult{}, ErrSearchForbidden
	}
	summaryCache := newSearchSummaryCache(s.summaryCallLimit, s.summaryTimeout)
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
	results, err := s.searchVisibleEvidence(ctx, query, vector, summaryCache)
	if err != nil {
		return SearchResult{}, err
	}
	documents, err := s.searchVisibleDocuments(ctx, query, vector, summaryCache)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Evidence: results, Documents: documents}, nil
}

func (s *Searcher) searchVisibleEvidence(ctx context.Context, query domain.SearchQuery, vector []float32, summaryCache *searchSummaryCache) ([]Evidence, error) {
	results := make([]Evidence, 0, query.Limit())
	for offset, pageLimit := 0, searchPageLimit(query.Limit()); len(results) < query.Limit() && offset < maximumSearchCandidates; offset += pageLimit {
		candidateLimit := searchCandidateLimit(pageLimit, offset)
		candidates, err := s.store.Search(ctx, query, vector, candidateLimit, offset)
		if err != nil {
			return nil, errors.New("search evidence")
		}
		candidateCount := len(candidates)
		candidates = s.filterVisibleEvidence(candidates)
		visible, err := s.visibility.FilterIndexed(ctx, candidates)
		if err != nil {
			return nil, errors.New("validate index visibility")
		}
		results = append(results, visible...)
		if candidateCount < candidateLimit {
			break
		}
	}
	sortEvidenceByScore(results)
	results = trimEvidence(results, query.Limit())
	s.populateEvidenceSummaries(ctx, query.Question(), results, summaryCache)
	return results, nil
}

func (s *Searcher) searchVisibleDocuments(ctx context.Context, query domain.SearchQuery, vector []float32, summaryCache *searchSummaryCache) ([]DocumentResult, error) {
	results := make([]DocumentResult, 0, query.Limit())
	for offset, pageLimit := 0, searchPageLimit(query.Limit()); len(results) < query.Limit() && offset < maximumSearchCandidates; offset += pageLimit {
		candidateLimit := searchCandidateLimit(pageLimit, offset)
		page, err := s.store.SearchDocuments(ctx, query, vector, candidateLimit, offset)
		if err != nil {
			return nil, errors.New("search documents")
		}
		page.Documents = s.filterVisibleDocuments(page.Documents)
		visible, err := s.visibility.FilterIndexedDocuments(ctx, page.Documents)
		if err != nil {
			return nil, errors.New("validate document visibility")
		}
		results = append(results, visible...)
		if page.Exhausted {
			break
		}
	}
	sortDocumentsByScore(results)
	results = trimDocuments(results, query.Limit())
	s.populateDocumentSummaries(ctx, query.Question(), results, summaryCache)
	return results, nil
}

func (s *Searcher) populateEvidenceSummaries(ctx context.Context, question string, results []Evidence, summaryCache *searchSummaryCache) {
	if len(results) == 0 {
		return
	}
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for index := range results {
		index := index
		group.Go(func() error {
			results[index].Summary = s.summarizeEvidence(groupContext, question, results[index], summaryCache)
			return nil
		})
	}
	_ = group.Wait()
}

func (s *Searcher) populateDocumentSummaries(ctx context.Context, question string, results []DocumentResult, summaryCache *searchSummaryCache) {
	if len(results) == 0 {
		return
	}
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for index := range results {
		index := index
		group.Go(func() error {
			for evidenceIndex := range results[index].Evidence {
				results[index].Evidence[evidenceIndex].Summary = s.summarizeEvidence(groupContext, question, results[index].Evidence[evidenceIndex], summaryCache)
			}
			results[index].Summary = s.summarizeDocument(groupContext, question, results[index], summaryCache)
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

func (s *Searcher) filterVisibleEvidence(values []Evidence) []Evidence {
	results := make([]Evidence, 0, len(values))
	for _, value := range values {
		if value.Score < s.minimumVisibleScore {
			continue
		}
		results = append(results, value)
	}
	return results
}

func (s *Searcher) filterVisibleDocuments(values []DocumentResult) []DocumentResult {
	results := make([]DocumentResult, 0, len(values))
	for _, value := range values {
		if value.Score < s.minimumVisibleScore {
			continue
		}
		value.Evidence = s.filterVisibleEvidence(value.Evidence)
		sortEvidenceByScore(value.Evidence)
		if len(value.Evidence) == 0 {
			continue
		}
		results = append(results, value)
	}
	return results
}

func sortEvidenceByScore(values []Evidence) {
	slices.SortStableFunc(values, func(left, right Evidence) int {
		switch {
		case left.Score > right.Score:
			return -1
		case left.Score < right.Score:
			return 1
		default:
			return 0
		}
	})
}

func sortDocumentsByScore(values []DocumentResult) {
	slices.SortStableFunc(values, func(left, right DocumentResult) int {
		switch {
		case left.Score > right.Score:
			return -1
		case left.Score < right.Score:
			return 1
		default:
			return 0
		}
	})
}

func (s *Searcher) summarizeEvidence(ctx context.Context, question string, value Evidence, summaryCache *searchSummaryCache) string {
	return s.summarizeText(ctx, SummaryRequest{Question: question, Passage: value.Passage}, summaryCache)
}

func (s *Searcher) summarizeDocument(ctx context.Context, question string, value DocumentResult, summaryCache *searchSummaryCache) string {
	input := normalizeDocumentSummaryInput(value)
	if input == "" {
		return ""
	}
	return s.summarizeText(ctx, SummaryRequest{Question: question, Passage: input}, summaryCache)
}

func (s *Searcher) summarizeText(ctx context.Context, request SummaryRequest, summaryCache *searchSummaryCache) string {
	if summaryCache == nil {
		return summarizeText(normalizeSummaryInput(request.Passage))
	}
	return summaryCache.summarize(ctx, s.summaryProvider, request)
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
	return strings.TrimSpace(strings.Join(strings.Fields(value), " "))
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
	return strings.TrimSpace(normalized)
}

type searchSummaryCache struct {
	mu             sync.Mutex
	remaining      int
	summaryTimeout time.Duration
	entries        map[string]*searchSummaryEntry
}

type searchSummaryEntry struct {
	ready   chan struct{}
	summary string
}

func newSearchSummaryCache(limit int, summaryTimeout time.Duration) *searchSummaryCache {
	return &searchSummaryCache{remaining: limit, summaryTimeout: summaryTimeout, entries: make(map[string]*searchSummaryEntry)}
}

func (c *searchSummaryCache) summarize(ctx context.Context, provider SummaryProvider, request SummaryRequest) string {
	normalizedPassage := normalizeSummaryInput(request.Passage)
	if normalizedPassage == "" {
		return ""
	}
	normalizedQuestion := normalizeSummaryInput(request.Question)
	if normalizedQuestion == "" {
		normalizedQuestion = request.Question
	}
	key := normalizedQuestion + "\x00" + normalizedPassage

	c.mu.Lock()
	if entry, ok := c.entries[key]; ok {
		ready := entry.ready
		c.mu.Unlock()
		<-ready
		return entry.summary
	}
	entry := &searchSummaryEntry{ready: make(chan struct{})}
	c.entries[key] = entry
	useProvider := provider != nil && c.remaining > 0
	if useProvider {
		c.remaining--
	}
	c.mu.Unlock()

	summary := summarizeText(normalizedPassage)
	if useProvider {
		summaryContext := ctx
		cancel := func() {}
		if c.summaryTimeout > 0 {
			summaryContext, cancel = context.WithTimeout(ctx, c.summaryTimeout)
		}
		defer cancel()
		if providerSummary, err := provider.Summarize(summaryContext, SummaryRequest{Question: normalizedQuestion, Passage: normalizedPassage}); err == nil {
			if sanitized := normalizeProviderSummary(providerSummary); sanitized != "" {
				summary = sanitized
			}
		}
	}

	c.mu.Lock()
	entry.summary = summary
	close(entry.ready)
	c.mu.Unlock()
	return summary
}

type SummaryProvider interface {
	Summarize(context.Context, SummaryRequest) (string, error)
}
