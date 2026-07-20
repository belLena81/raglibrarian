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
	store := &stubEvidenceStore{results: []Evidence{{EvidenceID: "evidence-1", BookID: "book-1", Title: "Systems", Passage: "Replication keeps copies.", Score: 0.91}}}
	searcher, err := NewSearcher(embedder, store, visibleIndexes{})
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}
	results, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: " replication ", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].EvidenceID != "evidence-1" || store.query.Question() != "replication" {
		t.Fatalf("unexpected results: %#v", results)
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
	calls   int
	query   domain.SearchQuery
	results []Evidence
	err     error
}

func (s *stubEvidenceStore) Search(_ context.Context, query domain.SearchQuery, _ []float32) ([]Evidence, error) {
	s.calls++
	s.query = query
	return s.results, s.err
}

type visibleIndexes struct{}

func (visibleIndexes) FilterIndexed(_ context.Context, values []Evidence) ([]Evidence, error) {
	return values, nil
}
