package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"golang.org/x/sync/errgroup"
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
	Evidence         []Evidence
	Documents        []DocumentResult
	QueryEmbedding   []float32
	EmbeddingProfile string
	RetrievalProfile string
	CorpusSnapshot   string
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
	assessmentCache   AssessmentCache
	cacheObserver     AssessmentCacheObserver
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
	AssessmentCache             AssessmentCachePolicy
	CandidatePageMultiplier     int
	ReciprocalRankFusionK       int
	MaximumAssessmentInputRunes int
	RequestPolicy               domain.SearchRequestPolicy
}

type SearcherOption func(*Searcher)

func WithAssessmentCache(cache AssessmentCache) SearcherOption {
	return func(searcher *Searcher) {
		searcher.assessmentCache = cache
	}
}

func WithAssessmentCacheObserver(observer AssessmentCacheObserver) SearcherOption {
	return func(searcher *Searcher) {
		searcher.cacheObserver = observer
	}
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
	options ...SearcherOption,
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
	searcher := &Searcher{
		embedder:          embedder,
		evidenceAssessor:  assessor,
		policy:            policy,
		analyzer:          heuristicQueryAnalyzer{policy: policy},
		chunkRetriever:    storeChunkRetriever{store: store, visibility: visibility, policy: policy},
		documentRetriever: storeDocumentRetriever{store: store, visibility: visibility, policy: policy},
		lexicalRetriever:  lexicalRetriever,
		fusion:            reciprocalRankFusion{k: policy.ReciprocalRankFusionK},
	}
	for _, option := range options {
		if option != nil {
			option(searcher)
		}
	}
	return searcher, nil
}

func (s *Searcher) Search(ctx context.Context, actor domain.Actor, input domain.SearchQueryInput) (SearchResult, error) {
	if !actor.CanSearch() {
		return SearchResult{}, ErrSearchForbidden
	}
	query, err := domain.NewSearchQuery(input, s.policy.RequestPolicy)
	if err != nil {
		return SearchResult{}, err
	}
	vector, err := s.embedder.EmbedQuery(ctx, query.Question())
	if err != nil {
		return SearchResult{}, searchOperationFailure("embed query", err)
	}
	if len(vector) != domain.EmbeddingDimensions {
		return SearchResult{}, errors.New("invalid embedding dimensions")
	}
	assessmentCache := newSearchAssessmentCache(s.policy.AssessmentCallLimit, s.policy.AssessmentTimeout, s.policy.MaximumAssessmentInputRunes, vector, s.policy.AssessmentCache, s.assessmentCache, s.cacheObserver)
	defer assessmentCache.report()
	plan := s.analyzer.Analyze(query)
	chunkCandidates, documentCandidates, lexicalCandidates, err := s.retrieveCandidates(ctx, query, vector, plan)
	if err != nil {
		return SearchResult{}, err
	}
	results, err := s.searchAcceptedEvidence(ctx, query, chunkCandidates, documentCandidates, lexicalCandidates, assessmentCache)
	if err != nil {
		return SearchResult{}, err
	}
	documents := documentsFromEvidence(results)
	documents = mergeDocumentMetadata(documents, documentCandidates)
	documents = trimDocuments(documents, query.Limit())
	profile := domain.SupportedIndexProfile()
	profileDigest := fmt.Sprintf("%x", profile.Digest)
	corpusSnapshot := responseSnapshot(results, documents)
	return SearchResult{
		Evidence:         results,
		Documents:        documents,
		QueryEmbedding:   append([]float32(nil), vector...),
		EmbeddingProfile: profile.Name + ":" + profileDigest,
		RetrievalProfile: profileDigest,
		CorpusSnapshot:   corpusSnapshot,
	}, nil
}

func (s *Searcher) retrieveCandidates(
	ctx context.Context,
	query domain.SearchQuery,
	vector []float32,
	plan RetrievalPlan,
) ([]Evidence, []DocumentResult, []Evidence, error) {
	var (
		chunkCandidates    []Evidence
		documentCandidates []DocumentResult
		lexicalCandidates  []Evidence
	)
	group, retrievalContext := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		chunkCandidates, err = s.chunkRetriever.Retrieve(retrievalContext, query, vector, plan)
		return err
	})
	group.Go(func() error {
		var err error
		documentCandidates, err = s.documentRetriever.Retrieve(retrievalContext, query, vector, plan)
		return err
	})
	if s.lexicalRetriever != nil {
		group.Go(func() error {
			var err error
			lexicalCandidates, err = s.lexicalRetriever.Retrieve(retrievalContext, query, plan)
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return nil, nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	return chunkCandidates, documentCandidates, lexicalCandidates, nil
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

// responseSnapshot identifies the current visibility-filtered Retrieval
// projection without retaining document text. Answer combines it with its own
// selected-evidence fingerprint before reusing a generated response.
func responseSnapshot(evidence []Evidence, documents []DocumentResult) string {
	values := make([]string, 0, len(evidence)+len(documents))
	hash := sha256.New()
	for _, value := range evidence {
		values = append(values, "e:"+value.EvidenceID)
	}
	for _, value := range documents {
		values = append(values, "d:"+value.DocumentID)
		for _, item := range value.Evidence {
			values = append(values, "de:"+value.DocumentID+":"+item.EvidenceID)
		}
	}
	slices.Sort(values)
	for _, value := range values {
		_, _ = hash.Write([]byte(value + "\n"))
	}
	return hex.EncodeToString(hash.Sum(nil))
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
) ([]Evidence, error) {
	candidates := fusedEvidenceCandidates(s.fusion, query, chunkCandidates, documentCandidates, lexicalCandidates)
	results := make([]Evidence, 0, query.Limit())
	seen := make(map[string]struct{})
	for _, value := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !ok || !assessment.Relevant {
			continue
		}
		value.Summary = assessment.Summary
		seen[value.EvidenceID] = struct{}{}
		results = append(results, value)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return trimEvidence(results, query.Limit()), nil
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
	queryEmbedding []float32
	cachePolicy    AssessmentCachePolicy
	cache          AssessmentCache
	observer       AssessmentCacheObserver
	entries        map[string]*searchAssessmentEntry
	stats          AssessmentCacheStats
}

type searchAssessmentEntry struct {
	ready      chan struct{}
	assessment EvidenceAssessment
	ok         bool
}

func newSearchAssessmentCache(limit int, summaryTimeout time.Duration, maximumRunes int, queryEmbedding []float32, cachePolicy AssessmentCachePolicy, cache AssessmentCache, observer AssessmentCacheObserver) *searchAssessmentCache {
	cachePolicy.MaximumInputRunes = maximumRunes
	return &searchAssessmentCache{
		remaining:      limit,
		localOnly:      limit == 0,
		summaryTimeout: summaryTimeout,
		maximumRunes:   maximumRunes,
		queryEmbedding: append([]float32(nil), queryEmbedding...),
		cachePolicy:    cachePolicy,
		cache:          cache,
		observer:       observer,
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
	c.mu.Unlock()

	lookup, cacheable := newAssessmentCacheLookup(c.cachePolicy, normalizedQuestion, normalizedPassage, c.queryEmbedding, time.Now())
	if cacheable && c.cache != nil {
		if cached, outcome, err := c.cache.Lookup(ctx, lookup); err == nil {
			c.observeLookup(outcome)
			if outcome == AssessmentCacheOutcomeHit || outcome == AssessmentCacheOutcomeNegativeHit {
				c.completeEntry(entry, cached, true)
				return cached, true
			}
		} else {
			c.observeLookup(AssessmentCacheOutcomeLookupError)
		}
	}

	c.mu.Lock()
	useProvider := assessor != nil && !c.providerFailed && c.remaining > 0
	useLocalAssessment := assessor == nil || c.localOnly || c.providerFailed || c.remaining == 0
	if useProvider {
		c.remaining--
		c.stats.ProviderCalls++
	}
	c.mu.Unlock()

	assessment, ok := localAssessment(normalizedPassage, c.maximumRunes)
	if useLocalAssessment {
		c.mu.Lock()
		c.stats.LocalFallbacks++
		c.mu.Unlock()
	}
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
			if cacheable && c.cache != nil {
				if err = c.cache.Store(ctx, newAssessmentCacheEntry(lookup, assessment, c.cachePolicy)); err != nil {
					c.observeStore(AssessmentCacheOutcomeStoreError)
				} else {
					c.observeStore(AssessmentCacheOutcomeStored)
				}
			}
		} else if ctx.Err() == nil {
			assessment, ok = localAssessment(normalizedPassage, c.maximumRunes)
			c.mu.Lock()
			c.providerFailed = true
			c.stats.LocalFallbacks++
			c.mu.Unlock()
		}
	}

	c.completeEntry(entry, assessment, ok)
	return assessment, ok
}

func (c *searchAssessmentCache) completeEntry(entry *searchAssessmentEntry, assessment EvidenceAssessment, ok bool) {
	c.mu.Lock()
	entry.assessment = assessment
	entry.ok = ok
	close(entry.ready)
	c.mu.Unlock()
}

func (c *searchAssessmentCache) observeLookup(outcome AssessmentCacheOutcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch outcome {
	case AssessmentCacheOutcomeHit:
		c.stats.Hits++
	case AssessmentCacheOutcomeNegativeHit:
		c.stats.NegativeHits++
	case AssessmentCacheOutcomeMiss:
		c.stats.Misses++
	case AssessmentCacheOutcomeSemanticMismatch:
		c.stats.SemanticMismatches++
	case AssessmentCacheOutcomeGuardMismatch:
		c.stats.GuardMismatches++
	case AssessmentCacheOutcomeLookupError:
		c.stats.LookupErrors++
	}
}

func (c *searchAssessmentCache) observeStore(outcome AssessmentCacheOutcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch outcome {
	case AssessmentCacheOutcomeStored:
		c.stats.Stores++
	case AssessmentCacheOutcomeStoreError:
		c.stats.StoreErrors++
	}
}

func (c *searchAssessmentCache) report() {
	if c.observer != nil {
		c.mu.Lock()
		stats := c.stats
		c.mu.Unlock()
		c.observer.AssessmentCacheSearch(stats)
	}
}

type EvidenceAssessor interface {
	Assess(context.Context, SummaryRequest) (EvidenceAssessment, error)
}
