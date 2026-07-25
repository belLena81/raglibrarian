package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

func TestSearcherAuthorizesBeforeCallingDependencies(t *testing.T) {
	embedder := &stubEmbedder{}
	store := &stubEvidenceStore{}
	searcher, err := NewSearcher(embedder, store, visibleIndexes{})
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}
	_, err = searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "pending"}, domain.SearchQueryInput{Question: "replication"})
	if !errors.Is(err, ErrSearchForbidden) {
		t.Fatalf("Search() error = %v, want ErrSearchForbidden", err)
	}
	if embedder.calls != 0 || store.calls != 0 {
		t.Fatalf("dependencies called before authorization: embedder=%d store=%d", embedder.calls, store.calls)
	}
}

func TestSearcherReturnsRankedEvidence(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		results:   []Evidence{{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Title: "Systems", Passage: "Replication keeps copies.", Score: 0.91}},
		documents: []DocumentResult{{DocumentID: "document-1", JobID: "job-1", BookID: "book-1", Title: "Systems", ChunkCount: 10, Evidence: []Evidence{{EvidenceID: "evidence-1", Passage: "Replication keeps copies.", Score: 0.91}}}},
	}
	searcher, err := NewSearcher(embedder, store, visibleIndexes{})
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}
	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: " replication ", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "evidence-1" || result.Evidence[0].Summary != "Replication keeps copies." ||
		len(result.Documents) != 1 || result.Documents[0].DocumentID != "document-1" || result.Documents[0].Summary != "Replication keeps copies." ||
		store.query.Question() != "replication" || embedder.calls != 1 {
		t.Fatalf("unexpected results: %#v", result)
	}
}

func TestSearcherUsesProviderSummariesAndFallsBack(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		results: []Evidence{{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Title: "Systems", Passage: "Deterministic retries keep search stable.", Score: 0.91}},
		documents: []DocumentResult{{
			DocumentID: "document-1", JobID: "job-1", BookID: "book-1", Title: "Systems", ChunkCount: 1,
			Evidence: []Evidence{{EvidenceID: "evidence-1", Passage: "Deterministic retries keep search stable.", Score: 0.91}},
		}},
	}
	searcher, err := NewSearcher(embedder, store, visibleIndexes{})
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}
	provider := &stubSummaryProvider{response: func(value SummaryRequest) string {
		return "summary: " + strings.TrimSpace(value.Question) + " | " + strings.TrimSpace(value.Passage)
	}}
	searcher.SetSummaryProvider(provider)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: " replication ", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Summary != "summary: replication | Deterministic retries keep search stable." {
		t.Fatalf("provider-backed evidence summary missing: %#v", result.Evidence)
	}
	if len(result.Documents) != 1 || result.Documents[0].Summary != "summary: replication | Deterministic retries keep search stable." || len(result.Documents[0].Evidence) != 1 ||
		result.Documents[0].Evidence[0].Summary != "summary: replication | Deterministic retries keep search stable." {
		t.Fatalf("provider-backed document summary missing: %#v", result.Documents)
	}
	if provider.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls())
	}
	if len(provider.requests) != 1 || provider.requests[0].Question != "replication" || provider.requests[0].Passage != "Deterministic retries keep search stable." {
		t.Fatalf("provider requests missing question/passage: %#v", provider.requests)
	}

	searcher.SetSummaryProvider(&stubSummaryProvider{err: errors.New("provider failed")})
	fallback, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: " replication ", Limit: 3})
	if err != nil {
		t.Fatalf("fallback Search() error = %v", err)
	}
	if len(fallback.Evidence) != 1 || fallback.Evidence[0].Summary != "Deterministic retries keep search stable." || len(fallback.Documents) != 1 || fallback.Documents[0].Summary != "Deterministic retries keep search stable." {
		t.Fatalf("local fallback summary missing: %#v", fallback)
	}
}

func TestSearcherCapsProviderSummaryCallsPerSearch(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	results := make([]Evidence, 0, 5)
	documents := make([]DocumentResult, 0, 5)
	for index := 0; index < 5; index++ {
		evidenceID := strings.Join([]string{"evidence", string(rune('a' + index))}, "-")
		passage := strings.Join([]string{"evidence", string(rune('a' + index)), "passage"}, " ")
		results = append(results, Evidence{
			EvidenceID: evidenceID,
			JobID:      "job-" + evidenceID,
			BookID:     "book-" + evidenceID,
			Title:      "Systems",
			Passage:    passage,
			Score:      0.91,
		})
		documentID := strings.Join([]string{"document", string(rune('a' + index))}, "-")
		docPassage := strings.Join([]string{"document", string(rune('a' + index)), "passage"}, " ")
		documents = append(documents, DocumentResult{
			DocumentID: documentID,
			JobID:      "job-" + documentID,
			BookID:     "book-" + documentID,
			Title:      "Systems",
			ChunkCount: 1,
			Evidence:   []Evidence{{EvidenceID: evidenceID + "-doc", Passage: docPassage, Score: 0.91}},
		})
	}
	store := &stubEvidenceStore{
		results:   results,
		documents: documents,
	}
	searcher, err := NewSearcher(embedder, store, visibleIndexes{})
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}
	provider := &stubSummaryProvider{response: func(value SummaryRequest) string {
		return "summary: " + strings.TrimSpace(value.Passage)
	}}
	searcher.SetSummaryProvider(provider)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 5 || len(result.Documents) != 5 {
		t.Fatalf("unexpected result counts: %#v", result)
	}
	if provider.calls() != 4 {
		t.Fatalf("provider calls = %d, want 4", provider.calls())
	}
}

func TestSearcherFiltersLowScoringEvidenceBeforeReturningResults(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		resultsByPage: [][]Evidence{
			{
				{EvidenceID: "low-1", JobID: "job-low-1", BookID: "book-low", Passage: "low evidence", Score: 0.59},
				{EvidenceID: "low-2", JobID: "job-low-2", BookID: "book-low", Passage: "low evidence", Score: 0.58},
			},
			{
				{EvidenceID: "high-1", JobID: "job-high-1", BookID: "book-high", Passage: "high evidence", Score: 0.60},
			},
		},
		documentsByPage: [][]DocumentResult{
			{
				{DocumentID: "doc-low", JobID: "job-low-1", BookID: "book-low", ChunkCount: 1, Score: 0.93, Evidence: []Evidence{{EvidenceID: "low-1", Passage: "low evidence", Score: 0.59}, {EvidenceID: "low-2", Passage: "low evidence", Score: 0.58}}},
			},
			{
				{DocumentID: "doc-high", JobID: "job-high-1", BookID: "book-high", ChunkCount: 1, Score: 0.91, Evidence: []Evidence{{EvidenceID: "high-1", Passage: "high evidence", Score: 0.60}}},
			},
		},
	}
	searcher, err := NewSearcher(embedder, store, visibleIndexes{})
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 1})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "high-1" || result.Evidence[0].Score < minimumVisibleScore {
		t.Fatalf("low-scoring evidence was not filtered: %#v", result.Evidence)
	}
	if len(result.Documents) != 1 || result.Documents[0].DocumentID != "doc-high" || len(result.Documents[0].Evidence) != 1 || result.Documents[0].Evidence[0].EvidenceID != "high-1" {
		t.Fatalf("low-scoring document evidence was not filtered: %#v", result.Documents)
	}
}

func TestSearcherSkipsEmptyPassagesForSummaries(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		results: []Evidence{{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Passage: "   ", Score: 0.91}},
		documents: []DocumentResult{{
			DocumentID: "document-1", JobID: "job-1", BookID: "book-1", ChunkCount: 1,
			Evidence: []Evidence{{EvidenceID: "evidence-1", Passage: "   ", Score: 0.91}},
		}},
	}
	searcher, err := NewSearcher(embedder, store, visibleIndexes{})
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}
	provider := &stubSummaryProvider{}
	searcher.SetSummaryProvider(provider)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: " replication ", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if provider.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls())
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Summary != "" || len(result.Documents) != 1 || result.Documents[0].Summary != "" {
		t.Fatalf("empty passages should not produce summaries: %#v", result)
	}
}

func TestSearcherBackfillsAfterVisibilityFiltering(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		resultsByPage: [][]Evidence{
			{
				{EvidenceID: "pending-1", JobID: "pending-1", BookID: "book-pending", Passage: "not visible", Score: 0.59},
				{EvidenceID: "pending-2", JobID: "pending-2", BookID: "book-pending", Passage: "not visible", Score: 0.58},
			},
			{
				{EvidenceID: "visible-1", JobID: "indexed-1", BookID: "book-1", Passage: "visible one", Score: 0.80},
				{EvidenceID: "visible-2", JobID: "indexed-2", BookID: "book-2", Passage: "visible two", Score: 0.79},
			},
		},
		documentsByPage: [][]DocumentResult{
			{
				{DocumentID: "pending-document-1", JobID: "pending-1", BookID: "book-pending", ChunkCount: 1, Evidence: []Evidence{{EvidenceID: "pending-1", Score: 0.99}}},
				{DocumentID: "pending-document-2", JobID: "pending-2", BookID: "book-pending", ChunkCount: 1, Evidence: []Evidence{{EvidenceID: "pending-2", Score: 0.98}}},
			},
			{
				{DocumentID: "visible-document-1", JobID: "indexed-1", BookID: "book-1", ChunkCount: 1, Evidence: []Evidence{{EvidenceID: "visible-1", Passage: "visible one", Score: 0.80}}},
				{DocumentID: "visible-document-2", JobID: "indexed-2", BookID: "book-2", ChunkCount: 1, Evidence: []Evidence{{EvidenceID: "visible-2", Passage: "visible two", Score: 0.79}}},
			},
		},
	}
	visibility := filteringVisibility{indexedJobs: map[string]struct{}{"indexed-1": {}, "indexed-2": {}}}
	searcher, err := NewSearcher(embedder, store, visibility)
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 1})

	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "visible-1" || len(result.Documents) != 1 || result.Documents[0].DocumentID != "visible-document-1" {
		t.Fatalf("unexpected visible results: %#v", result)
	}
	if store.calls != 2 || store.documentCalls != 2 {
		t.Fatalf("search pages evidence/documents = %d/%d, want 2/2", store.calls, store.documentCalls)
	}
	if len(store.requests) != 4 || store.requests[0].limit != 2 || store.requests[0].offset != 0 || store.requests[1].limit != 2 || store.requests[1].offset != 2 {
		t.Fatalf("unexpected paging requests: %#v", store.requests)
	}
}

func TestSearcherContinuesDocumentPaginationAfterHydrationDropsCandidates(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{documentPages: []DocumentPage{
		{Exhausted: false},
		{Documents: []DocumentResult{{DocumentID: "document-2", JobID: "job-2", BookID: "book-2", ChunkCount: 1, Evidence: []Evidence{{EvidenceID: "evidence-2", Score: 0.8}}}}, Exhausted: true},
	}}
	searcher, err := NewSearcher(embedder, store, visibleIndexes{})
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 1})

	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Documents) != 1 || result.Documents[0].DocumentID != "document-2" {
		t.Fatalf("documents = %#v", result.Documents)
	}
	if store.documentCalls != 2 {
		t.Fatalf("document calls = %d, want 2", store.documentCalls)
	}
}

type stubEmbedder struct {
	calls  int
	vector []float32
	err    error
}

func (s *stubEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	s.calls++
	return s.vector, s.err
}

type stubEvidenceStore struct {
	calls           int
	documentCalls   int
	query           domain.SearchQuery
	results         []Evidence
	documents       []DocumentResult
	resultsByPage   [][]Evidence
	documentsByPage [][]DocumentResult
	documentPages   []DocumentPage
	requests        []searchRequest
	err             error
}

type searchRequest struct {
	limit  int
	offset int
}

func (s *stubEvidenceStore) Search(_ context.Context, query domain.SearchQuery, _ []float32, limit, offset int) ([]Evidence, error) {
	s.calls++
	s.query = query
	s.requests = append(s.requests, searchRequest{limit: limit, offset: offset})
	if len(s.resultsByPage) > 0 {
		index := offset / limit
		if index < len(s.resultsByPage) {
			return s.resultsByPage[index], s.err
		}
		return nil, s.err
	}
	return s.results, s.err
}

func (s *stubEvidenceStore) SearchDocuments(_ context.Context, query domain.SearchQuery, _ []float32, limit, offset int) (DocumentPage, error) {
	s.documentCalls++
	s.query = query
	s.requests = append(s.requests, searchRequest{limit: limit, offset: offset})
	if len(s.documentPages) > 0 {
		index := offset / limit
		if index < len(s.documentPages) {
			return s.documentPages[index], s.err
		}
		return DocumentPage{Exhausted: true}, s.err
	}
	if len(s.documentsByPage) > 0 {
		index := offset / limit
		if index < len(s.documentsByPage) {
			return DocumentPage{Documents: s.documentsByPage[index], Exhausted: index == len(s.documentsByPage)-1}, s.err
		}
		return DocumentPage{Exhausted: true}, s.err
	}
	return DocumentPage{Documents: s.documents, Exhausted: true}, s.err
}

type visibleIndexes struct{}

func (visibleIndexes) FilterIndexed(_ context.Context, values []Evidence) ([]Evidence, error) {
	return values, nil
}

func (visibleIndexes) FilterIndexedDocuments(_ context.Context, values []DocumentResult) ([]DocumentResult, error) {
	return values, nil
}

type filteringVisibility struct {
	indexedJobs map[string]struct{}
}

func (v filteringVisibility) FilterIndexed(_ context.Context, values []Evidence) ([]Evidence, error) {
	results := make([]Evidence, 0, len(values))
	for _, value := range values {
		if _, indexed := v.indexedJobs[value.JobID]; indexed {
			results = append(results, value)
		}
	}
	return results, nil
}

func (v filteringVisibility) FilterIndexedDocuments(_ context.Context, values []DocumentResult) ([]DocumentResult, error) {
	results := make([]DocumentResult, 0, len(values))
	for _, value := range values {
		if _, indexed := v.indexedJobs[value.JobID]; indexed {
			results = append(results, value)
		}
	}
	return results, nil
}

type stubSummaryProvider struct {
	mu       sync.Mutex
	requests []SummaryRequest
	response func(SummaryRequest) string
	err      error
}

func (s *stubSummaryProvider) Summarize(_ context.Context, value SummaryRequest) (string, error) {
	s.mu.Lock()
	s.requests = append(s.requests, value)
	response := s.response
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if response == nil {
		return value.Passage, nil
	}
	return response(value), nil
}

func (s *stubSummaryProvider) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}
