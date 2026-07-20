package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

func TestQdrantSearchUsesBoundedLimitAndReturnsEvidence(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		var body queryRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Limit != 2 || body.Filter == nil || len(body.Filter.Must) == 0 || body.Filter.Must[0].Key != "indexed" || body.Filter.Must[0].Match.Value != "true" || body.ScoreThreshold <= 0 {
			t.Fatalf("unexpected request body: %#v, %v", body, err)
		}
		return response(http.StatusOK, `{"result":{"points":[{"id":"point-1","score":0.9,"payload":{"evidence_id":"evidence-1","chunk_id":"chunk-1","job_id":"job-1","book_id":"book-1","title":"Systems","author":"Author","year":2026,"tags":["distributed"],"chapter":"One","section":"Replication","page_start":3,"page_end":4,"passage":"Copies improve availability."}}]}}`)
	})}

	store, err := NewQdrant("http://qdrant.test", "evidence", client)
	if err != nil {
		t.Fatalf("NewQdrant() error = %v", err)
	}
	query, _ := domain.NewSearchQuery(domain.SearchQueryInput{Question: "replication", Limit: 2})
	results, err := store.Search(context.Background(), query, make([]float32, domain.EmbeddingDimensions))
	if err != nil || len(results) != 1 || results[0].EvidenceID != "evidence-1" {
		t.Fatalf("Search() = %#v, %v", results, err)
	}
}

func TestQdrantEnsureCollectionCreatesExactSchema(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		requests++
		switch requests {
		case 1:
			return response(http.StatusNotFound, `{}`)
		case 2:
			var body map[string]map[string]any
			if request.Method != http.MethodPut || json.NewDecoder(request.Body).Decode(&body) != nil || body["vectors"]["size"] != float64(domain.EmbeddingDimensions) || body["vectors"]["distance"] != "Cosine" {
				t.Fatalf("unexpected collection creation request: %s %#v", request.Method, body)
			}
			return response(http.StatusOK, `{}`)
		default:
			return response(http.StatusOK, `{"result":{"config":{"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`)
		}
	})}
	store, _ := NewQdrant("http://qdrant.test", "evidence", client)
	if err := store.EnsureCollection(context.Background()); err != nil || requests != 3 {
		t.Fatalf("EnsureCollection() requests=%d error=%v", requests, err)
	}
}

func TestQdrantStagesBeforeActivatingJob(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		requests++
		contents, _ := io.ReadAll(request.Body)
		if requests == 1 {
			if !bytes.Contains(contents, []byte(`"indexed":"false"`)) || !bytes.Contains(contents, []byte(`"job_id":"job-1"`)) {
				t.Fatalf("upsert did not stage the job: %s", contents)
			}
		} else if !bytes.Contains(contents, []byte(`"indexed":"true"`)) || !bytes.Contains(contents, []byte(`"job_id"`)) {
			t.Fatalf("activation did not target staged job: %s", contents)
		}
		return response(http.StatusOK, `{}`)
	})}
	store, _ := NewQdrant("http://qdrant.test", "evidence", client)
	record := application.EvidenceRecord{Evidence: application.Evidence{EvidenceID: "book-1:chunk-1", ChunkID: "chunk-1", BookID: "book-1", Passage: "evidence"}, JobID: "job-1", Vector: make([]float32, domain.EmbeddingDimensions)}
	if err := store.Upsert(context.Background(), []application.EvidenceRecord{record}); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateJob(context.Background(), "job-1"); err != nil || requests != 2 {
		t.Fatalf("ActivateJob() requests=%d error=%v", requests, err)
	}
}

type roundTripFunc func(*http.Request) *http.Response

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request), nil
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}
}
