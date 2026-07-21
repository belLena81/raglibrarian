package application

import (
	"context"
	"errors"
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
		documents: []DocumentResult{{DocumentID: "document-1", JobID: "job-1", BookID: "book-1", Title: "Systems", ChunkCount: 10, Evidence: []Evidence{{EvidenceID: "evidence-1"}}}},
	}
	searcher, err := NewSearcher(embedder, store, visibleIndexes{})
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}
	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: " replication ", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "evidence-1" || len(result.Documents) != 1 || result.Documents[0].DocumentID != "document-1" || store.query.Question() != "replication" || embedder.calls != 1 {
		t.Fatalf("unexpected results: %#v", result)
	}
}

func TestSearcherBackfillsAfterVisibilityFiltering(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		resultsByPage: [][]Evidence{
			{
				{EvidenceID: "pending-1", JobID: "pending-1", BookID: "book-pending", Passage: "not visible", Score: 0.99},
				{EvidenceID: "pending-2", JobID: "pending-2", BookID: "book-pending", Passage: "not visible", Score: 0.98},
			},
			{
				{EvidenceID: "visible-1", JobID: "indexed-1", BookID: "book-1", Passage: "visible one", Score: 0.80},
				{EvidenceID: "visible-2", JobID: "indexed-2", BookID: "book-2", Passage: "visible two", Score: 0.79},
			},
		},
		documentsByPage: [][]DocumentResult{
			{
				{DocumentID: "pending-document-1", JobID: "pending-1", BookID: "book-pending", ChunkCount: 1, Evidence: []Evidence{{EvidenceID: "pending-1"}}},
				{DocumentID: "pending-document-2", JobID: "pending-2", BookID: "book-pending", ChunkCount: 1, Evidence: []Evidence{{EvidenceID: "pending-2"}}},
			},
			{
				{DocumentID: "visible-document-1", JobID: "indexed-1", BookID: "book-1", ChunkCount: 1, Evidence: []Evidence{{EvidenceID: "visible-1"}}},
				{DocumentID: "visible-document-2", JobID: "indexed-2", BookID: "book-2", ChunkCount: 1, Evidence: []Evidence{{EvidenceID: "visible-2"}}},
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
		{Documents: []DocumentResult{{DocumentID: "document-2", JobID: "job-2", BookID: "book-2", ChunkCount: 1, Evidence: []Evidence{{EvidenceID: "evidence-2"}}}}, Exhausted: true},
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
