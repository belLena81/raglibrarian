package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
)

type fakeRetriever struct {
	mu     sync.Mutex
	result domain.SearchResult
	err    error
	calls  atomic.Int32
	input  domain.SearchRequest
}

func (f *fakeRetriever) Search(_ context.Context, input domain.SearchRequest) (domain.SearchResult, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.input = input
	return f.result, f.err
}
func (f *fakeRetriever) CheckReady(context.Context) error { return f.err }

type fakeProvider struct {
	segments []domain.AnswerSegment
	err      error
	calls    atomic.Int32
	block    <-chan struct{}
	input    GeneratorRequest
}

func (f *fakeProvider) Generate(ctx context.Context, input GeneratorRequest) ([]domain.AnswerSegment, error) {
	f.calls.Add(1)
	f.input = input
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.segments, f.err
}

func TestAnswerSelectsBoundedPrimaryEvidenceForSynthesis(t *testing.T) {
	limits := testLimits()
	limits.MaximumEvidence = 2
	limits.MaximumEvidenceBytes = 8
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"e-1"}}}}
	search := domain.SearchResult{
		Results: []domain.Evidence{{
			EvidenceID: "e-1",
			Passage:    "first",
			Book:       domain.BookMetadata{Title: "Book", Author: "Author"},
			PageStart:  10,
			PageEnd:    12,
		}, {EvidenceID: "e-2", Passage: "second"}},
		Documents: []domain.DocumentResult{{Evidence: []domain.Evidence{{EvidenceID: "e-1", Passage: "duplicate"}, {EvidenceID: "e-2", Passage: "second"}}}},
	}
	service := newTestService(t, &fakeRetriever{result: search}, provider, limits)
	result, err := service.Answer(context.Background(), validRequest())
	if err != nil || result.Answer == nil || len(provider.input.Evidence) != 2 || provider.input.Evidence[0].EvidenceID != "e-1" ||
		provider.input.Evidence[0].Title != "Book" || provider.input.Evidence[0].PageStart != 10 || provider.input.Evidence[1].EvidenceID != "e-2" {
		t.Fatalf("Answer() = %#v, %v; context=%#v", result, err, provider.input.Evidence)
	}
	if result.Answer.Segments[0].Text != "This is described in Book by Author, pages 10-12: answer" {
		t.Fatalf("answer text = %q", result.Answer.Segments[0].Text)
	}
}

func TestAnswerPrefersDiverseEvidenceBeforeFillingByScore(t *testing.T) {
	limits := testLimits()
	limits.MaximumEvidence = 2
	search := domain.SearchResult{
		Results: []domain.Evidence{
			{
				EvidenceID: "same-section-high",
				Passage:    "high scoring first passage",
				Book:       domain.BookMetadata{BookID: "book-1", Title: "Systems"},
				Chapter:    "One",
				Section:    "Retries",
			},
			{
				EvidenceID: "same-section-second",
				Passage:    "second passage from the same section",
				Book:       domain.BookMetadata{BookID: "book-1", Title: "Systems"},
				Chapter:    "One",
				Section:    "Retries",
			},
			{
				EvidenceID: "different-section",
				Passage:    "different section passage",
				Book:       domain.BookMetadata{BookID: "book-1", Title: "Systems"},
				Chapter:    "Two",
				Section:    "Queues",
			},
		},
	}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"same-section-high", "different-section"}}}}
	service := newTestService(t, &fakeRetriever{result: search}, provider, limits)

	result, err := service.Answer(context.Background(), validRequest())

	if err != nil || result.Answer == nil {
		t.Fatalf("Answer() = %#v, %v", result, err)
	}
	if len(provider.input.Evidence) != 2 ||
		provider.input.Evidence[0].EvidenceID != "same-section-high" ||
		provider.input.Evidence[1].EvidenceID != "different-section" {
		t.Fatalf("provider evidence = %#v", provider.input.Evidence)
	}
}

func TestAnswerFillsRemainingEvidenceAfterDiversityPass(t *testing.T) {
	limits := testLimits()
	limits.MaximumEvidence = 2
	search := domain.SearchResult{
		Results: []domain.Evidence{
			{
				EvidenceID: "first",
				Passage:    "first passage",
				Book:       domain.BookMetadata{BookID: "book-1"},
				Section:    "Retries",
			},
			{
				EvidenceID: "second",
				Passage:    "second passage",
				Book:       domain.BookMetadata{BookID: "book-1"},
				Section:    "Retries",
			},
		},
	}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"first", "second"}}}}
	service := newTestService(t, &fakeRetriever{result: search}, provider, limits)

	result, err := service.Answer(context.Background(), validRequest())

	if err != nil || result.Answer == nil {
		t.Fatalf("Answer() = %#v, %v", result, err)
	}
	if len(provider.input.Evidence) != 2 ||
		provider.input.Evidence[0].EvidenceID != "first" ||
		provider.input.Evidence[1].EvidenceID != "second" {
		t.Fatalf("provider evidence = %#v", provider.input.Evidence)
	}
}

type fakeObserver struct{}

func (o *fakeObserver) Observe(Outcome, time.Duration)                 {}
func (o *fakeObserver) Failure(Outcome, string, string, time.Duration) {}
func (o *fakeObserver) GeneratorStarted()                              {}
func (o *fakeObserver) GeneratorResponse(int, int)                     {}
func (o *fakeObserver) GeneratorFinished()                             {}
func (o *fakeObserver) CacheLookup(CacheOutcome)                       {}

func TestAnswerReturnsValidatedGroundedSegments(t *testing.T) {
	retriever := &fakeRetriever{result: searchResult("evidence-1")}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: " Grounded answer ", EvidenceIDs: []string{"evidence-1"}}}}
	service := newTestService(t, retriever, provider, testLimits())
	result, err := service.Answer(context.Background(), validRequest())
	if err != nil || result.Answer == nil || result.Answer.Segments[0].Text != "Grounded answer" || result.Summary != "Grounded answer" {
		t.Fatalf("Answer() = %#v, %v", result, err)
	}
}

func TestAnswerUsesNormalizedRequestForRetrievalAndGeneration(t *testing.T) {
	retriever := &fakeRetriever{result: searchResult("evidence-1")}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}}
	service := newTestService(t, retriever, provider, testLimits())
	request := validRequest()
	request.Question = "  question  "
	request.Filters.Author = "  author  "
	request.Filters.Tags = []string{" history ", " science "}

	result, err := service.Answer(context.Background(), request)

	if err != nil || result.Answer == nil {
		t.Fatalf("Answer() = %#v, %v", result, err)
	}
	if retriever.input.Question != "question" || retriever.input.Filters.Author != "author" ||
		len(retriever.input.Filters.Tags) != 2 || retriever.input.Filters.Tags[0] != "history" ||
		retriever.input.Filters.Tags[1] != "science" {
		t.Fatalf("retriever request = %#v", retriever.input)
	}
	if provider.input.Question != "question" {
		t.Fatalf("provider question = %q", provider.input.Question)
	}
}

func TestAnswerFiltersEvidenceBeforeSynthesisUsingMinimumScore(t *testing.T) {
	retriever := &fakeRetriever{result: domain.SearchResult{
		Query: "question",
		Results: []domain.Evidence{
			{EvidenceID: "low", Passage: "too weak", Score: 0.59},
			{EvidenceID: "high", Passage: "strong enough", Score: 0.6},
		},
		Documents: []domain.DocumentResult{{
			DocumentID: "doc-1",
			Evidence: []domain.Evidence{
				{EvidenceID: "low-doc", Passage: "too weak", Score: 0.59},
				{EvidenceID: "high-doc", Passage: "strong enough", Score: 0.6},
			},
		}},
	}}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"high"}}}}
	service := newTestService(t, retriever, provider, testLimits())
	request := validRequest()
	request.MinimumEvidenceScore = 0.6

	result, err := service.Answer(context.Background(), request)
	if err != nil || result.Answer == nil {
		t.Fatalf("Answer() = %#v, %v", result, err)
	}
	if len(provider.input.Evidence) != 1 || provider.input.Evidence[0].EvidenceID != "high" {
		t.Fatalf("provider evidence = %#v", provider.input.Evidence)
	}
	if len(result.Search.Results) != 1 || result.Search.Results[0].EvidenceID != "high" {
		t.Fatalf("search results = %#v", result.Search.Results)
	}
	if len(result.Search.Documents) != 1 || len(result.Search.Documents[0].Evidence) != 1 || result.Search.Documents[0].Evidence[0].EvidenceID != "high-doc" {
		t.Fatalf("search documents = %#v", result.Search.Documents)
	}
}

func TestAnswerFallsBackToTopDocumentEvidenceWhenPrimaryResultsAreEmpty(t *testing.T) {
	retriever := &fakeRetriever{result: domain.SearchResult{
		Query: "question",
		Documents: []domain.DocumentResult{{
			DocumentID: "doc-1",
			Evidence: []domain.Evidence{{
				EvidenceID: "doc-evidence",
				Passage:    "document evidence",
				Book:       domain.BookMetadata{Title: "Document Book"},
				PageStart:  3,
				PageEnd:    3,
			}},
		}},
	}}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "document synopsis", EvidenceIDs: []string{"doc-evidence"}}}}
	service := newTestService(t, retriever, provider, testLimits())

	result, err := service.Answer(context.Background(), validRequest())
	if err != nil || result.Answer == nil || len(provider.input.Evidence) != 1 || provider.input.Evidence[0].EvidenceID != "doc-evidence" {
		t.Fatalf("Answer() = %#v, %v; context=%#v", result, err, provider.input.Evidence)
	}
	if result.Answer.Segments[0].Text != "This is described in Document Book, page 3: document synopsis" {
		t.Fatalf("answer text = %q", result.Answer.Segments[0].Text)
	}
}

func TestAnswerDoesNotCallProviderWithoutEvidence(t *testing.T) {
	provider := &fakeProvider{}
	service := newTestService(t, &fakeRetriever{result: domain.SearchResult{}}, provider, testLimits())
	result, err := service.Answer(context.Background(), validRequest())
	if err != nil || result.Answer != nil || provider.calls.Load() != 0 {
		t.Fatalf("Answer() = %#v, %v; calls=%d", result, err, provider.calls.Load())
	}
}

func TestAnswerDegradesForProviderAndCitationFailures(t *testing.T) {
	tests := []*fakeProvider{
		{err: errors.New("provider failed")},
		{segments: []domain.AnswerSegment{{Text: "unsupported", EvidenceIDs: []string{"unknown"}}}},
		{segments: []domain.AnswerSegment{{Text: "duplicate", EvidenceIDs: []string{"evidence-1", "evidence-1"}}}},
	}
	for index, provider := range tests {
		service := newTestService(t, &fakeRetriever{result: searchResult("evidence-1")}, provider, testLimits())
		result, err := service.Answer(context.Background(), validRequest())
		if err != nil || result.Answer != nil || len(result.Search.Results) != 1 {
			t.Fatalf("case %d: %#v, %v", index, result, err)
		}
	}
}

func TestAnswerReturnsOuterCancellationAfterGeneratorFailure(t *testing.T) {
	tests := []struct {
		name       string
		newContext func() (context.Context, context.CancelFunc)
		want       error
	}{
		{
			name: "canceled",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			want: context.Canceled,
		},
		{
			name: "deadline exceeded",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			want: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.newContext()
			cancel()
			provider := &fakeProvider{err: errors.New("provider failed")}
			service := newTestService(t, &fakeRetriever{result: searchResult("evidence-1")}, provider, testLimits())

			result, err := service.Answer(ctx, validRequest())

			if !errors.Is(err, test.want) || result.Answer != nil {
				t.Fatalf("Answer() = %#v, %v; want %v", result, err, test.want)
			}
		})
	}
}

func TestAnswerDegradesForGeneratorOwnedDeadline(t *testing.T) {
	provider := &fakeProvider{err: context.DeadlineExceeded}
	service := newTestService(t, &fakeRetriever{result: searchResult("evidence-1")}, provider, testLimits())

	result, err := service.Answer(context.Background(), validRequest())

	if err != nil || result.Answer != nil || len(result.Search.Results) != 1 {
		t.Fatalf("Answer() = %#v, %v", result, err)
	}
}

func TestSelectEvidenceChargesAllContextStringFields(t *testing.T) {
	limits := testLimits()
	limits.MaximumContextBytes = 31
	search := domain.SearchResult{Results: []domain.Evidence{
		{
			EvidenceID: "e-1",
			Passage:    "passage",
			Book:       domain.BookMetadata{Title: "title", Author: "author"},
			Chapter:    "chapter",
			Section:    "section",
		},
		{EvidenceID: "e-2", Passage: "second"},
	}}

	selected := selectEvidence(search, limits)

	if len(selected) != 1 || selected[0].EvidenceID != "e-2" {
		t.Fatalf("selectEvidence() = %#v", selected)
	}
}

func TestSelectEvidenceRejectsInvalidUTF8InEveryContextStringField(t *testing.T) {
	invalid := string([]byte{0xff})
	tests := []struct {
		name   string
		mutate func(*domain.Evidence)
	}{
		{name: "evidence ID", mutate: func(value *domain.Evidence) { value.EvidenceID = invalid }},
		{name: "passage", mutate: func(value *domain.Evidence) { value.Passage = invalid }},
		{name: "title", mutate: func(value *domain.Evidence) { value.Book.Title = invalid }},
		{name: "author", mutate: func(value *domain.Evidence) { value.Book.Author = invalid }},
		{name: "chapter", mutate: func(value *domain.Evidence) { value.Chapter = invalid }},
		{name: "section", mutate: func(value *domain.Evidence) { value.Section = invalid }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := domain.Evidence{
				EvidenceID: "evidence-1",
				Passage:    "passage",
				Book:       domain.BookMetadata{Title: "title", Author: "author"},
				Chapter:    "chapter",
				Section:    "section",
			}
			test.mutate(&value)

			if selected := selectEvidence(domain.SearchResult{Results: []domain.Evidence{value}}, testLimits()); len(selected) != 0 {
				t.Fatalf("selectEvidence() = %#v, want none", selected)
			}
		})
	}
}

func TestAnswerBoundsContextAndUsesNonBlockingConcurrency(t *testing.T) {
	limits := testLimits()
	limits.GeneratorConcurrency = 1
	block := make(chan struct{})
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}, block: block}
	service := newTestService(t, &fakeRetriever{result: searchResult("evidence-1")}, provider, limits)
	done := make(chan struct{})
	go func() {
		_, _ = service.Answer(context.Background(), validRequest())
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for provider.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	result, err := service.Answer(context.Background(), validRequest())
	if err != nil || result.Answer != nil || time.Since(started) > 100*time.Millisecond || provider.calls.Load() != 1 {
		t.Fatalf("saturated Answer() = %#v, %v; calls=%d", result, err, provider.calls.Load())
	}
	close(block)
	<-done
}

func TestAnswerRejectsOversizedOrMixedValidityOutput(t *testing.T) {
	limits := testLimits()
	limits.MaximumAnswerBytes = 4
	providers := []*fakeProvider{
		{segments: []domain.AnswerSegment{{Text: "large", EvidenceIDs: []string{"evidence-1"}}}},
		{segments: []domain.AnswerSegment{{Text: "ok", EvidenceIDs: []string{"evidence-1"}}, {Text: "bad", EvidenceIDs: []string{"unknown"}}}},
		{segments: []domain.AnswerSegment{{Text: "unsafe\u202eanswer", EvidenceIDs: []string{"evidence-1"}}}},
		{segments: []domain.AnswerSegment{{Text: "unsafe\nanswer", EvidenceIDs: []string{"evidence-1"}}}},
	}
	for _, provider := range providers {
		service := newTestService(t, &fakeRetriever{result: searchResult("evidence-1")}, provider, limits)
		result, err := service.Answer(context.Background(), validRequest())
		if err != nil || result.Answer != nil {
			t.Fatalf("Answer() = %#v, %v", result, err)
		}
	}
}

func TestAnswerCacheHitsForNormalizedQueryAndKeepsRetrievalLive(t *testing.T) {
	retriever := &fakeRetriever{result: cachedSearch("evidence-1")}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}}
	service := newCachedTestService(t, retriever, provider)

	first := validRequest()
	first.Question = "Concurrency in Node.js"
	if _, err := service.Answer(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := validRequest()
	second.Question = "  concurrency IN node js! "
	result, err := service.Answer(context.Background(), second)

	if err != nil || result.Answer == nil || provider.calls.Load() != 1 || retriever.calls.Load() != 2 {
		t.Fatalf("Answer() = %#v, %v; provider calls=%d retrieval calls=%d", result, err, provider.calls.Load(), retriever.calls.Load())
	}
}

func TestAnswerCacheHitsForTopicExpansionWhenModeIsCompatible(t *testing.T) {
	retriever := &fakeRetriever{result: cachedSearch("evidence-1")}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}}
	service := newCachedTestService(t, retriever, provider)

	first := validRequest()
	first.Question = "Concurrency in Node.js"
	if _, err := service.Answer(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := validRequest()
	second.Question = "Explain handling concurrency in Node.js"
	if _, err := service.Answer(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want cache hit", provider.calls.Load())
	}
}

func TestAnswerCacheMissesWhenCachedTopicIsMoreSpecificThanCurrentQuery(t *testing.T) {
	retriever := &fakeRetriever{result: cachedSearch("evidence-1")}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}}
	service := newCachedTestService(t, retriever, provider)

	first := validRequest()
	first.Question = "Node.js event loop concurrency"
	if _, err := service.Answer(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := validRequest()
	second.Question = "Concurrency in Node.js"
	changedSearch := cachedSearch("evidence-1")
	changedSearch.QueryEmbedding = []float32{0.8, 0.6}
	retriever.result = changedSearch
	if _, err := service.Answer(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want cache miss", provider.calls.Load())
	}
}

func TestAnswerCacheMissesForIncompatibleAnswerMode(t *testing.T) {
	retriever := &fakeRetriever{result: cachedSearch("evidence-1")}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}}
	service := newCachedTestService(t, retriever, provider)

	first := validRequest()
	first.Question = "Concurrency in Node.js"
	if _, err := service.Answer(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := validRequest()
	second.Question = "Give me examples of handling concurrency in Node.js"
	if _, err := service.Answer(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want cache miss", provider.calls.Load())
	}
}

func TestAnswerCacheHitsForSemanticOnlyParaphrase(t *testing.T) {
	retriever := &fakeRetriever{result: cachedSearch("evidence-1")}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}}
	service := newCachedTestService(t, retriever, provider)

	first := validRequest()
	first.Question = "Concurrency in Node.js"
	if _, err := service.Answer(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := validRequest()
	second.Question = "Parallel async coordination"
	if _, err := service.Answer(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want semantic-only cache hit", provider.calls.Load())
	}
}

func TestAnswerCacheMissesWhenGuardTokensDiffer(t *testing.T) {
	retriever := &fakeRetriever{result: cachedSearch("evidence-1")}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}}
	service := newCachedTestService(t, retriever, provider)

	first := validRequest()
	first.Question = "Concurrency in Node 18"
	if _, err := service.Answer(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := validRequest()
	second.Question = "Concurrency in Node 20"
	if _, err := service.Answer(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want guard-token cache miss", provider.calls.Load())
	}
}

func TestAnswerCacheClassifiesLexicalOverlapAndSemanticOnlyHits(t *testing.T) {
	cache := newTestAnswerCache(t, CachePolicy{
		Capacity:                   4,
		TTL:                        time.Minute,
		MinimumCosine:              0.9,
		SemanticOnlyMinimumCosine:  0.99,
		MinimumLexicalTopicOverlap: 0.7,
		GeneratorProfile:           "generator-v1",
	})
	request := validRequest()
	request.Question = "Node.js concurrency queues"
	search := cachedSearch("evidence-1")
	evidence := selectEvidence(search, testLimits())
	segments := []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}
	cache.store(request, search, evidence, segments)

	lexicalRequest := validRequest()
	lexicalRequest.Question = "Node.js concurrency workers"
	_, lexicalOutcome := cache.lookup(lexicalRequest, search, evidence)
	if lexicalOutcome != CacheOutcomeLexicalHit {
		t.Fatalf("lexical lookup outcome = %q, want %q", lexicalOutcome, CacheOutcomeLexicalHit)
	}

	semanticRequest := validRequest()
	semanticRequest.Question = "Parallel async coordination"
	_, semanticOutcome := cache.lookup(semanticRequest, search, evidence)
	if semanticOutcome != CacheOutcomeSemanticOnlyHit {
		t.Fatalf("semantic lookup outcome = %q, want %q", semanticOutcome, CacheOutcomeSemanticOnlyHit)
	}
}

func TestAnswerCacheTouchesHitsForLRUEviction(t *testing.T) {
	cache := newTestAnswerCache(t, CachePolicy{
		Capacity:                   2,
		TTL:                        time.Minute,
		MinimumCosine:              0.9,
		SemanticOnlyMinimumCosine:  0.99,
		MinimumLexicalTopicOverlap: 0.8,
		GeneratorProfile:           "generator-v1",
	})
	evidenceSearch := cachedSearch("evidence-1")
	evidence := selectEvidence(evidenceSearch, testLimits())
	segments := []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}
	a := validRequest()
	a.Question = "alpha"
	b := validRequest()
	b.Question = "beta"
	c := validRequest()
	c.Question = "gamma"
	searchA := cacheSearchWithEmbedding([]float32{1, 0})
	searchB := cacheSearchWithEmbedding([]float32{0, 1})
	searchC := cacheSearchWithEmbedding([]float32{-1, 0})
	cache.store(a, searchA, evidence, segments)
	cache.store(b, searchB, evidence, segments)
	if _, outcome := cache.lookup(a, searchA, evidence); outcome != CacheOutcomeHit {
		t.Fatalf("lookup A outcome = %q, want hit", outcome)
	}
	cache.store(c, searchC, evidence, segments)
	if _, outcome := cache.lookup(b, searchB, evidence); outcome == CacheOutcomeHit {
		t.Fatal("B was returned after LRU eviction")
	}
	if _, outcome := cache.lookup(a, searchA, evidence); outcome != CacheOutcomeHit {
		t.Fatalf("lookup A after eviction outcome = %q, want hit", outcome)
	}
}

func TestAnswerCacheReturnsMatchedEntryAfterLRUTouch(t *testing.T) {
	cache := newTestAnswerCache(t, CachePolicy{
		Capacity:                   2,
		TTL:                        time.Minute,
		MinimumCosine:              0.9,
		SemanticOnlyMinimumCosine:  0.99,
		MinimumLexicalTopicOverlap: 0.8,
		GeneratorProfile:           "generator-v1",
	})
	evidenceSearch := cachedSearch("evidence-1")
	evidence := selectEvidence(evidenceSearch, testLimits())
	alpha := validRequest()
	alpha.Question = "alpha"
	beta := validRequest()
	beta.Question = "beta"
	searchAlpha := cacheSearchWithEmbedding([]float32{1, 0})
	searchBeta := cacheSearchWithEmbedding([]float32{0, 1})
	cache.store(alpha, searchAlpha, evidence, []domain.AnswerSegment{{Text: "alpha answer", EvidenceIDs: []string{"evidence-1"}}})
	cache.store(beta, searchBeta, evidence, []domain.AnswerSegment{{Text: "beta answer", EvidenceIDs: []string{"evidence-1"}}})

	entry, outcome := cache.lookup(alpha, searchAlpha, evidence)
	if outcome != CacheOutcomeHit {
		t.Fatalf("lookup outcome = %q, want hit", outcome)
	}
	if len(entry.segments) != 1 || entry.segments[0].Text != "alpha answer" {
		t.Fatalf("lookup returned segments = %#v, want alpha answer", entry.segments)
	}
}

func TestAnswerCacheHitsWhenCurrentEvidenceAddsUncitedEvidence(t *testing.T) {
	retriever := &fakeRetriever{result: cachedSearch("evidence-1")}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}}
	service := newCachedTestService(t, retriever, provider)

	if _, err := service.Answer(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	changed := cachedSearch("evidence-1")
	changed.Results = append([]domain.Evidence{
		{EvidenceID: "extra", Passage: "extra trusted evidence", Score: 1},
	}, changed.Results...)
	retriever.result = changed
	if _, err := service.Answer(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}

	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want cache hit", provider.calls.Load())
	}
}

func TestAnswerCacheMissesWhenHardMetadataChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.SearchResult, *domain.SearchRequest)
	}{
		{name: "corpus snapshot", mutate: func(search *domain.SearchResult, _ *domain.SearchRequest) { search.CorpusSnapshot = "snapshot-2" }},
		{name: "selected evidence", mutate: func(search *domain.SearchResult, _ *domain.SearchRequest) {
			search.Results[0].Passage = "new trusted evidence"
		}},
		{name: "filters", mutate: func(_ *domain.SearchResult, request *domain.SearchRequest) {
			request.Filters.Tags = []string{"history"}
		}},
		{name: "authorization scope", mutate: func(_ *domain.SearchResult, request *domain.SearchRequest) {
			request.Actor.UserID = "user-2"
		}},
		{name: "minimum evidence score", mutate: func(_ *domain.SearchResult, request *domain.SearchRequest) {
			request.MinimumEvidenceScore = 0.8
		}},
		{name: "retrieval profile", mutate: func(search *domain.SearchResult, _ *domain.SearchRequest) { search.RetrievalProfile = "hybrid-v2" }},
		{name: "embedding profile", mutate: func(search *domain.SearchResult, _ *domain.SearchRequest) { search.EmbeddingProfile = "embedding-v2" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			retriever := &fakeRetriever{result: cachedSearch("evidence-1")}
			provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}}
			service := newCachedTestService(t, retriever, provider)
			if _, err := service.Answer(context.Background(), validRequest()); err != nil {
				t.Fatal(err)
			}
			changedRequest := validRequest()
			changedSearch := cachedSearch("evidence-1")
			test.mutate(&changedSearch, &changedRequest)
			retriever.result = changedSearch
			if _, err := service.Answer(context.Background(), changedRequest); err != nil {
				t.Fatal(err)
			}
			if provider.calls.Load() != 2 {
				t.Fatalf("provider calls = %d, want cache miss", provider.calls.Load())
			}
		})
	}
}

func TestAnswerCoalescesConcurrentIdenticalCacheMisses(t *testing.T) {
	retriever := &fakeRetriever{result: cachedSearch("evidence-1")}
	block := make(chan struct{})
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}, block: block}
	service := newCachedTestService(t, retriever, provider)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			result, err := service.Answer(context.Background(), validRequest())
			if err == nil && result.Answer == nil {
				err = errors.New("missing answer")
			}
			results <- err
		}()
	}

	close(start)
	deadline := time.Now().Add(time.Second)
	for provider.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(block)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one coalesced generation", provider.calls.Load())
	}
}

func TestCachedSegmentsMatchCurrentSelectedEvidence(t *testing.T) {
	segments := []domain.AnswerSegment{{Text: "validated", EvidenceIDs: []string{"evidence-1"}}}
	evidence := []domain.ContextEvidence{{EvidenceID: "evidence-1"}}
	if !cachedSegmentsMatchEvidence(segments, evidence) {
		t.Fatal("expected current evidence to validate cached segment")
	}
	if cachedSegmentsMatchEvidence(segments, []domain.ContextEvidence{{EvidenceID: "evidence-2"}}) {
		t.Fatal("cached segment must not be returned without current cited evidence")
	}
}

func TestCanonicalFiltersKeepsControlCharacterValuesDistinct(t *testing.T) {
	left := domain.SearchFilters{Tags: []string{"a\x00b"}, Author: "c"}
	right := domain.SearchFilters{Tags: []string{"a", "b"}, Author: "c"}
	if canonicalFilters(left) == canonicalFilters(right) {
		t.Fatal("distinct filters must not share a cache key")
	}
}

func newTestService(t *testing.T, retriever Retriever, generator AnswerGenerator, limits Limits) *Service {
	t.Helper()
	service, err := NewService(retriever, generator, &fakeObserver{}, limits, testRequestPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newCachedTestService(t *testing.T, retriever Retriever, generator AnswerGenerator) *Service {
	t.Helper()
	service, err := NewService(
		retriever,
		generator,
		&fakeObserver{},
		testLimits(),
		testRequestPolicy(),
		CachePolicy{
			Capacity:                   8,
			TTL:                        time.Minute,
			MinimumCosine:              0.9,
			SemanticOnlyMinimumCosine:  0.99,
			MinimumLexicalTopicOverlap: 0.8,
			GeneratorProfile:           "generator-v1",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newTestAnswerCache(t *testing.T, policy CachePolicy) *answerCache {
	t.Helper()
	cache, err := newAnswerCache(policy)
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func testRequestPolicy() domain.RequestPolicy {
	return domain.RequestPolicy{
		MaximumQuestionCharacters: 2000,
		MaximumFilterTags:         20,
		MaximumTagCharacters:      64,
		MaximumAuthorCharacters:   256,
		MaximumResultLimit:        20,
	}
}

func testLimits() Limits {
	return Limits{
		MaximumEvidence:      8,
		MaximumContextBytes:  32 << 10,
		MaximumEvidenceBytes: 8 << 10,
		MaximumSegments:      8,
		MaximumAnswerBytes:   8 << 10,
		MaximumSummaryRunes:  512,
		MaximumCitations:     8,
		MaximumOutputTokens:  768,
		GeneratorConcurrency: 4,
		RequestTimeout:       5 * time.Minute,
		RetrievalTimeout:     4*time.Minute + 45*time.Second,
		GeneratorTimeout:     4*time.Minute + 30*time.Second,
	}
}

func validRequest() domain.SearchRequest {
	return domain.SearchRequest{Question: "question", Limit: 5, Actor: domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, CorrelationID: strings.Repeat("a", 32)}
}

func searchResult(id string) domain.SearchResult {
	return domain.SearchResult{Query: "question", Results: []domain.Evidence{{EvidenceID: id, Passage: "trusted evidence"}}}
}

func cachedSearch(id string) domain.SearchResult {
	return domain.SearchResult{
		Query:            "question",
		QueryEmbedding:   []float32{0.6, 0.8},
		EmbeddingProfile: "embedding-v1",
		RetrievalProfile: "hybrid-v1",
		CorpusSnapshot:   "snapshot-1",
		Results:          []domain.Evidence{{EvidenceID: id, Passage: "trusted evidence", Score: 1}},
	}
}

func cacheSearchWithEmbedding(embedding []float32) domain.SearchResult {
	search := cachedSearch("evidence-1")
	search.QueryEmbedding = embedding
	return search
}
