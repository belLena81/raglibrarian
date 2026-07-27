package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
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

type EvidenceAssessment struct {
	Relevant bool
	Summary  string
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
	assessmentCache := newSearchAssessmentCache(s.summaryCallLimit, s.summaryTimeout)
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
	results, err := s.searchVisibleEvidence(ctx, query, vector, assessmentCache)
	if err != nil {
		return SearchResult{}, err
	}
	documents := documentsFromEvidence(results)
	return SearchResult{Evidence: results, Documents: documents}, nil
}

func (s *Searcher) searchVisibleEvidence(ctx context.Context, query domain.SearchQuery, vector []float32, assessmentCache *searchAssessmentCache) ([]Evidence, error) {
	results := make([]Evidence, 0, query.Limit())
	seen := make(map[string]struct{})
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
		for _, value := range visible {
			if len(results) >= query.Limit() {
				break
			}
			if value.EvidenceID == "" {
				continue
			}
			if _, found := seen[value.EvidenceID]; found {
				continue
			}
			assessment, ok := s.assessEvidence(ctx, query.Question(), value, assessmentCache)
			if !ok || !assessment.Relevant {
				continue
			}
			value.Summary = assessment.Summary
			seen[value.EvidenceID] = struct{}{}
			results = append(results, value)
		}
		if candidateCount < candidateLimit {
			break
		}
	}
	sortEvidenceByScore(results)
	results = trimEvidence(results, query.Limit())
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

func (s *Searcher) assessEvidence(ctx context.Context, question string, value Evidence, assessmentCache *searchAssessmentCache) (EvidenceAssessment, bool) {
	if assessmentCache == nil {
		return localAssessment(value.Passage)
	}
	return assessmentCache.assess(ctx, s.summaryProvider, SummaryRequest{Question: question, Passage: value.Passage})
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

func summarizeText(value string) string {
	normalized := strings.Join(strings.Fields(value), " ")
	if normalized == "" {
		return ""
	}
	return strings.TrimSpace(normalized)
}

func localAssessment(passage string) (EvidenceAssessment, bool) {
	summary := summarizeText(normalizeSummaryInput(passage))
	if summary == "" {
		return EvidenceAssessment{}, false
	}
	return EvidenceAssessment{Relevant: true, Summary: summary}, true
}

func documentsFromEvidence(values []Evidence) []DocumentResult {
	documents := make([]DocumentResult, 0, len(values))
	byDocumentID := make(map[string]int)
	for _, evidence := range values {
		documentID := evidence.BookID + ":" + evidence.JobID
		if documentID == ":" {
			continue
		}
		index, found := byDocumentID[documentID]
		if !found {
			document := DocumentResult{
				DocumentID: documentID,
				JobID:      evidence.JobID,
				BookID:     evidence.BookID,
				Title:      evidence.Title,
				Author:     evidence.Author,
				MediaType:  evidence.MediaType,
				Year:       evidence.Year,
				Tags:       append([]string{}, evidence.Tags...),
				ChunkCount: 1,
				PageStart:  evidence.PageStart,
				PageEnd:    evidence.PageEnd,
				Score:      evidence.Score,
				Evidence:   []Evidence{evidence},
			}
			byDocumentID[documentID] = len(documents)
			documents = append(documents, document)
			continue
		}
		document := &documents[index]
		document.Evidence = append(document.Evidence, evidence)
		document.ChunkCount = uint32(len(document.Evidence))
		if evidence.Score > document.Score {
			document.Score = evidence.Score
		}
		if document.PageStart == 0 || (evidence.PageStart > 0 && evidence.PageStart < document.PageStart) {
			document.PageStart = evidence.PageStart
		}
		if evidence.PageEnd > document.PageEnd {
			document.PageEnd = evidence.PageEnd
		}
	}
	sortDocumentsByScore(documents)
	return documents
}

type searchAssessmentCache struct {
	mu             sync.Mutex
	remaining      int
	summaryTimeout time.Duration
	entries        map[string]*searchAssessmentEntry
}

type searchAssessmentEntry struct {
	ready      chan struct{}
	assessment EvidenceAssessment
	ok         bool
}

func newSearchAssessmentCache(limit int, summaryTimeout time.Duration) *searchAssessmentCache {
	return &searchAssessmentCache{remaining: limit, summaryTimeout: summaryTimeout, entries: make(map[string]*searchAssessmentEntry)}
}

func (c *searchAssessmentCache) assess(ctx context.Context, provider SummaryProvider, request SummaryRequest) (EvidenceAssessment, bool) {
	normalizedPassage := normalizeSummaryInput(request.Passage)
	if normalizedPassage == "" {
		return EvidenceAssessment{}, false
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
		return entry.assessment, entry.ok
	}
	entry := &searchAssessmentEntry{ready: make(chan struct{})}
	c.entries[key] = entry
	useProvider := provider != nil && c.remaining > 0
	if useProvider {
		c.remaining--
	}
	c.mu.Unlock()

	assessment, ok := localAssessment(normalizedPassage)
	if provider != nil && !useProvider {
		assessment = EvidenceAssessment{}
		ok = false
	} else if useProvider {
		assessment = EvidenceAssessment{}
		ok = false
		assessmentContext := ctx
		cancel := func() {}
		if c.summaryTimeout > 0 {
			assessmentContext, cancel = context.WithTimeout(ctx, c.summaryTimeout)
		}
		defer cancel()
		if providerAssessment, err := provider.Assess(assessmentContext, SummaryRequest{Question: normalizedQuestion, Passage: normalizedPassage}); err == nil {
			providerAssessment.Summary = normalizeProviderSummary(providerAssessment.Summary)
			assessment = providerAssessment
			ok = true
		}
	}

	c.mu.Lock()
	entry.assessment = assessment
	entry.ok = ok
	close(entry.ready)
	c.mu.Unlock()
	return assessment, ok
}

type SummaryProvider interface {
	Assess(context.Context, SummaryRequest) (EvidenceAssessment, error)
}
