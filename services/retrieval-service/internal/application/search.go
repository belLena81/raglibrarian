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

type LexicalEvidenceStore interface {
	SearchLexical(context.Context, domain.SearchQuery, int, int) ([]Evidence, error)
}

type IndexVisibility interface {
	FilterIndexed(context.Context, []Evidence) ([]Evidence, error)
	FilterIndexedDocuments(context.Context, []DocumentResult) ([]DocumentResult, error)
}

type Searcher struct {
	embedder          QueryEmbedder
	evidenceAssessor  EvidenceAssessor
	policy            SearchPolicy
	analyzer          QueryAnalyzer
	chunkRetriever    ChunkRetriever
	documentRetriever DocumentRetriever
	lexicalRetriever  LexicalRetriever
	fusion            CandidateFusion
}

type SummaryRequest struct {
	Question string
	Passage  string
}

type EvidenceAssessment struct {
	Relevant bool
	Summary  string
}

type SearchPolicy struct {
	MinimumVisibleScore         float64
	AssessmentCallLimit         int
	AssessmentTimeout           time.Duration
	CandidatePageMultiplier     int
	ReciprocalRankFusionK       int
	MaximumAssessmentInputRunes int
	RequestPolicy               domain.SearchRequestPolicy
}

func NewSearcherWithPolicy(embedder QueryEmbedder, store EvidenceStore, visibility IndexVisibility, assessor EvidenceAssessor, policy SearchPolicy) (*Searcher, error) {
	return NewSearcherWithPolicyAndLexical(embedder, store, nil, visibility, assessor, policy)
}

func NewSearcherWithPolicyAndLexical(
	embedder QueryEmbedder,
	store EvidenceStore,
	lexicalStore LexicalEvidenceStore,
	visibility IndexVisibility,
	assessor EvidenceAssessor,
	policy SearchPolicy,
) (*Searcher, error) {
	if embedder == nil || store == nil || visibility == nil || policy.AssessmentCallLimit < 0 ||
		policy.CandidatePageMultiplier < 1 || policy.ReciprocalRankFusionK < 1 ||
		policy.MaximumAssessmentInputRunes < 1 ||
		policy.RequestPolicy.MaximumQuestionCharacters <= 0 || policy.RequestPolicy.MaximumFilterTags <= 0 ||
		policy.RequestPolicy.MaximumTagCharacters <= 0 || policy.RequestPolicy.MaximumAuthorCharacters <= 0 ||
		policy.RequestPolicy.DefaultResultLimit <= 0 || policy.RequestPolicy.MaximumResultLimit <= 0 ||
		policy.RequestPolicy.DefaultResultLimit > policy.RequestPolicy.MaximumResultLimit {
		return nil, errors.New("invalid searcher configuration")
	}
	var lexicalRetriever LexicalRetriever
	if lexicalStore != nil {
		lexicalRetriever = storeLexicalRetriever{store: lexicalStore, visibility: visibility}
	}
	return &Searcher{
		embedder:          embedder,
		evidenceAssessor:  assessor,
		policy:            policy,
		analyzer:          heuristicQueryAnalyzer{policy: policy},
		chunkRetriever:    storeChunkRetriever{store: store, visibility: visibility, policy: policy},
		documentRetriever: storeDocumentRetriever{store: store, visibility: visibility, policy: policy},
		lexicalRetriever:  lexicalRetriever,
		fusion:            reciprocalRankFusion{k: policy.ReciprocalRankFusionK},
	}, nil
}

func (s *Searcher) SetEvidenceAssessor(assessor EvidenceAssessor) {
	s.evidenceAssessor = assessor
}

func (s *Searcher) SetEvidenceAssessorTimeout(timeout time.Duration) {
	s.policy.AssessmentTimeout = timeout
}

func (s *Searcher) Search(ctx context.Context, actor domain.Actor, input domain.SearchQueryInput) (SearchResult, error) {
	if !actor.CanSearch() {
		return SearchResult{}, ErrSearchForbidden
	}
	assessmentCache := newSearchAssessmentCache(s.policy.AssessmentCallLimit, s.policy.AssessmentTimeout, s.policy.MaximumAssessmentInputRunes)
	query, err := domain.NewSearchQuery(input, s.policy.RequestPolicy)
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
	plan := s.analyzer.Analyze(query)
	var (
		chunkCandidates    []Evidence
		documentCandidates []DocumentResult
		lexicalCandidates  []Evidence
		chunkErr           error
		documentErr        error
		lexicalErr         error
	)
	var work sync.WaitGroup
	retrievalCount := 2
	if s.lexicalRetriever != nil {
		retrievalCount++
	}
	work.Add(retrievalCount)
	go func() {
		defer work.Done()
		chunkCandidates, chunkErr = s.chunkRetriever.Retrieve(ctx, query, vector, plan)
	}()
	go func() {
		defer work.Done()
		documentCandidates, documentErr = s.documentRetriever.Retrieve(ctx, query, vector, plan)
	}()
	if s.lexicalRetriever != nil {
		go func() {
			defer work.Done()
			lexicalCandidates, lexicalErr = s.lexicalRetriever.Retrieve(ctx, query, plan)
		}()
	}
	work.Wait()
	if chunkErr != nil {
		return SearchResult{}, chunkErr
	}
	if documentErr != nil {
		return SearchResult{}, documentErr
	}
	if lexicalErr != nil {
		return SearchResult{}, lexicalErr
	}
	results := s.searchAcceptedEvidence(ctx, query, chunkCandidates, documentCandidates, lexicalCandidates, assessmentCache)
	documents := documentsFromEvidence(results)
	documents = mergeDocumentMetadata(documents, documentCandidates)
	documents = trimDocuments(documents, query.Limit())
	return SearchResult{Evidence: results, Documents: documents}, nil
}

func searchPageLimit(limit, candidatePageMultiplier int) int {
	pageLimit := limit * candidatePageMultiplier
	maximumSearchCandidates := searchCandidateBudget(limit, candidatePageMultiplier)
	if pageLimit > maximumSearchCandidates {
		return maximumSearchCandidates
	}
	return pageLimit
}

func searchCandidateBudget(maximumResultLimit, candidatePageMultiplier int) int {
	return maximumResultLimit * candidatePageMultiplier * candidatePageMultiplier
}

func searchCandidateLimit(pageLimit, offset, maximumSearchCandidates int) int {
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
		return localAssessment(value.Passage, s.policy.MaximumAssessmentInputRunes)
	}
	return assessmentCache.assess(ctx, s.evidenceAssessor, SummaryRequest{Question: question, Passage: value.Passage})
}

func (s *Searcher) searchAcceptedEvidence(
	ctx context.Context,
	query domain.SearchQuery,
	chunkCandidates []Evidence,
	documentCandidates []DocumentResult,
	lexicalCandidates []Evidence,
	assessmentCache *searchAssessmentCache,
) []Evidence {
	candidates := fusedEvidenceCandidates(s.fusion, query, chunkCandidates, documentCandidates, lexicalCandidates)
	results := make([]Evidence, 0, query.Limit())
	seen := make(map[string]struct{})
	for _, value := range candidates {
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
	return trimEvidence(results, query.Limit())
}

func normalizeSummaryInput(value string, maximumRunes int) string {
	normalized := strings.Join(strings.Fields(value), " ")
	if normalized == "" {
		return ""
	}
	if utf8.RuneCountInString(normalized) <= maximumRunes {
		return normalized
	}
	runes := []rune(normalized)
	return strings.TrimSpace(string(runes[:maximumRunes]))
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

func localAssessment(passage string, maximumRunes int) (EvidenceAssessment, bool) {
	summary := summarizeText(normalizeSummaryInput(passage, maximumRunes))
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
				ChunkCount: 0,
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
	localOnly      bool
	providerFailed bool
	summaryTimeout time.Duration
	maximumRunes   int
	entries        map[string]*searchAssessmentEntry
}

type searchAssessmentEntry struct {
	ready      chan struct{}
	assessment EvidenceAssessment
	ok         bool
}

func newSearchAssessmentCache(limit int, summaryTimeout time.Duration, maximumRunes int) *searchAssessmentCache {
	return &searchAssessmentCache{
		remaining:      limit,
		localOnly:      limit == 0,
		summaryTimeout: summaryTimeout,
		maximumRunes:   maximumRunes,
		entries:        make(map[string]*searchAssessmentEntry),
	}
}

func (c *searchAssessmentCache) assess(ctx context.Context, assessor EvidenceAssessor, request SummaryRequest) (EvidenceAssessment, bool) {
	if ctx.Err() != nil {
		return EvidenceAssessment{}, false
	}
	normalizedPassage := normalizeSummaryInput(request.Passage, c.maximumRunes)
	if normalizedPassage == "" {
		return EvidenceAssessment{}, false
	}
	normalizedQuestion := normalizeSummaryInput(request.Question, c.maximumRunes)
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
	useProvider := assessor != nil && !c.providerFailed && c.remaining > 0
	useLocalAssessment := assessor == nil || c.localOnly || c.providerFailed || c.remaining == 0
	if useProvider {
		c.remaining--
	}
	c.mu.Unlock()

	assessment, ok := localAssessment(normalizedPassage, c.maximumRunes)
	if !useLocalAssessment && !useProvider {
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
		if providerAssessment, err := assessor.Assess(assessmentContext, SummaryRequest{Question: normalizedQuestion, Passage: normalizedPassage}); err == nil {
			providerAssessment.Summary = normalizeProviderSummary(providerAssessment.Summary)
			assessment = providerAssessment
			ok = true
		} else if ctx.Err() == nil {
			assessment, ok = localAssessment(normalizedPassage, c.maximumRunes)
			c.mu.Lock()
			c.providerFailed = true
			c.mu.Unlock()
		}
	}

	c.mu.Lock()
	entry.assessment = assessment
	entry.ok = ok
	close(entry.ready)
	c.mu.Unlock()
	return assessment, ok
}

type EvidenceAssessor interface {
	Assess(context.Context, SummaryRequest) (EvidenceAssessment, error)
}
