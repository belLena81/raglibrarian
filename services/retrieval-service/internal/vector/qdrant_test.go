package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

func TestQdrantSearchUsesBoundedLimitAndReturnsEvidence(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		var body queryRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Limit != 4 || body.Offset != 6 || body.Filter == nil || len(body.Filter.Must) < 2 || body.Filter.Must[0].Key != "indexed" || body.Filter.Must[0].Match.Value != "true" || body.Filter.Must[1].Key != "vector_kind" || body.Filter.Must[1].Match.Value != "chunk" || body.ScoreThreshold <= 0 {
			t.Fatalf("unexpected request body: %#v, %v", body, err)
		}
		return response(http.StatusOK, `{"result":{"points":[{"id":"point-1","score":0.9,"payload":{"evidence_id":"evidence-1","chunk_id":"chunk-1","job_id":"job-1","book_id":"book-1","title":"Systems","author":"Author","year":2026,"tags":["distributed"],"chapter":"One","section":"Replication","page_start":3,"page_end":4,"passage":"Copies improve availability."}}]}}`)
	})}

	store, err := NewQdrant("http://qdrant.test", "evidence", client)
	if err != nil {
		t.Fatalf("NewQdrant() error = %v", err)
	}
	query, _ := domain.NewSearchQuery(domain.SearchQueryInput{Question: "replication", Limit: 2})
	results, err := store.Search(context.Background(), query, make([]float32, domain.EmbeddingDimensions), 4, 6)
	if err != nil || len(results) != 1 || results[0].EvidenceID != "evidence-1" {
		t.Fatalf("Search() = %#v, %v", results, err)
	}
}

func TestQdrantSearchDocumentsHydratesStoredChunkEvidence(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		requests++
		switch requests {
		case 1:
			var body queryRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("invalid document request body: %v", err)
			}
			if body.Limit != 4 || body.Offset != 2 || body.Filter.Must[1].Key != "vector_kind" || body.Filter.Must[1].Match.Value != "document" {
				t.Fatalf("unexpected document request: %#v", body)
			}
			return response(http.StatusOK, `{"result":{"points":[{"id":"document-point-1","score":0.8,"payload":{"document_id":"document-1","job_id":"job-1","book_id":"book-1","title":"Systems","author":"Author","year":2026,"tags":["distributed"],"page_start":1,"page_end":20,"chunk_count":10}},{"id":"document-point-2","score":0.7,"payload":{"document_id":"document-2","job_id":"job-2","book_id":"book-2","title":"Queues","author":"Author","year":2025,"tags":["distributed"],"page_start":2,"page_end":12,"chunk_count":8}}]}}`)
		case 2:
			var body queryBatchRequest
			if request.URL.Path != "/collections/evidence/points/query/batch" || json.NewDecoder(request.Body).Decode(&body) != nil || len(body.Searches) != 2 {
				t.Fatalf("unexpected evidence batch request: %s %#v", request.URL.Path, body)
			}
			for index, search := range body.Searches {
				wantJobID := "job-1"
				if index == 1 {
					wantJobID = "job-2"
				}
				if search.Limit != 3 || len(search.Filter.Must) < 3 || search.Filter.Must[1].Match.Value != "chunk" || search.Filter.Must[2].Key != "job_id" || search.Filter.Must[2].Match.Value != wantJobID {
					t.Fatalf("unexpected evidence hydration request %d: %#v", index, search)
				}
			}
			return response(http.StatusOK, `{"result":[{"points":[{"id":"point-1","score":0.9,"payload":{"evidence_id":"evidence-1","chunk_id":"chunk-1","job_id":"job-1","book_id":"book-1","title":"Systems","author":"Author","year":2026,"tags":["distributed"],"chapter":"One","section":"Replication","page_start":3,"page_end":4,"passage":"Copies improve availability."}}]},{"points":[{"id":"point-2","score":0.85,"payload":{"evidence_id":"evidence-2","chunk_id":"chunk-2","job_id":"job-2","book_id":"book-2","title":"Queues","author":"Author","year":2025,"tags":["distributed"],"chapter":"Two","section":"Ordering","page_start":5,"page_end":6,"passage":"Queues preserve work."}}]}]}`)
		default:
			t.Fatalf("unexpected request %d", requests)
			return response(http.StatusInternalServerError, `{}`)
		}
	})}
	store, _ := NewQdrant("http://qdrant.test", "evidence", client)
	query, _ := domain.NewSearchQuery(domain.SearchQueryInput{Question: "replication", Limit: 2})

	page, err := store.SearchDocuments(context.Background(), query, make([]float32, domain.EmbeddingDimensions), 4, 2)

	if err != nil || len(page.Documents) != 2 || page.Documents[0].DocumentID != "document-1" || len(page.Documents[0].Evidence) != 1 || !page.Exhausted || requests != 2 {
		t.Fatalf("SearchDocuments() = %#v, %v", page, err)
	}
}

func TestQdrantSearchDocumentsKeepsFullRawPageOpenAfterHydrationDrops(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		requests++
		switch requests {
		case 1:
			return response(http.StatusOK, `{"result":{"points":[{"id":"document-point-1","score":0.8,"payload":{"document_id":"document-1","job_id":"job-1","book_id":"book-1","chunk_count":1}},{"id":"document-point-2","score":0.7,"payload":{"document_id":"document-2","job_id":"job-2","book_id":"book-2","chunk_count":1}}]}}`)
		case 2:
			return response(http.StatusOK, `{"result":[{"points":[]},{"points":[]}]}`)
		default:
			t.Fatalf("unexpected request %d", requests)
			return response(http.StatusInternalServerError, `{}`)
		}
	})}
	store, _ := NewQdrant("http://qdrant.test", "evidence", client)
	query, _ := domain.NewSearchQuery(domain.SearchQueryInput{Question: "replication", Limit: 1})

	page, err := store.SearchDocuments(context.Background(), query, make([]float32, domain.EmbeddingDimensions), 2, 0)

	if err != nil || len(page.Documents) != 0 || page.Exhausted || requests != 2 {
		t.Fatalf("SearchDocuments() = %#v, %v", page, err)
	}
}

func TestQdrantSearchEvidenceBatchAcceptsHydrationResponseLargerThanFourMiB(t *testing.T) {
	passage := strings.Repeat("e", maximumQdrantResponseBytes)
	batchResponse := `{"result":[{"points":[{"score":0.9,"payload":{"evidence_id":"evidence-1","chunk_id":"chunk-1","job_id":"job-1","book_id":"book-1","passage":"` + passage + `"}}]}]}`
	if len(batchResponse) <= maximumQdrantResponseBytes || len(batchResponse) >= maximumEvidenceBatchResponseBytes {
		t.Fatalf("batch response size = %d, want between %d and %d", len(batchResponse), maximumQdrantResponseBytes, maximumEvidenceBatchResponseBytes)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		if request.URL.Path != "/collections/evidence/points/query/batch" {
			t.Fatalf("request path = %q", request.URL.Path)
		}
		return response(http.StatusOK, batchResponse)
	})}
	store, _ := NewQdrant("http://qdrant.test", "evidence", client)
	query, _ := domain.NewSearchQuery(domain.SearchQueryInput{Question: "replication", Limit: 1})

	results, err := store.searchEvidenceBatch(context.Background(), query, make([]float32, domain.EmbeddingDimensions), []string{"job-1"}, 3)

	if err != nil || len(results) != 1 || len(results[0]) != 1 || results[0][0].Passage != passage {
		t.Fatalf("searchEvidenceBatch() = %#v, %v", results, err)
	}
}

func TestQdrantPreservesExplicitZeroYearUpperBound(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		var body queryRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("invalid request body: %v", err)
		}
		var found bool
		for _, condition := range body.Filter.Must {
			if condition.Key == "year" {
				found = true
				if condition.Range == nil || condition.Range.LessThanOrEqual == nil || *condition.Range.LessThanOrEqual != 0 {
					t.Fatalf("explicit year_to=0 was not preserved: %#v", condition)
				}
			}
		}
		if !found {
			t.Fatalf("year filter missing from request: %#v", body.Filter.Must)
		}
		return response(http.StatusOK, `{"result":{"points":[]}}`)
	})}
	store, _ := NewQdrant("http://qdrant.test", "evidence", client)
	yearTo := 0
	query, err := domain.NewSearchQuery(domain.SearchQueryInput{Question: "old books", Filters: domain.SearchFilters{YearTo: &yearTo}, Limit: 1})
	if err != nil {
		t.Fatalf("NewSearchQuery() error = %v", err)
	}

	if _, err = store.Search(context.Background(), query, make([]float32, domain.EmbeddingDimensions), 1, 0); err != nil {
		t.Fatalf("Search() error = %v", err)
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
			if request.Method != http.MethodPut || json.NewDecoder(request.Body).Decode(&body) != nil || body["vectors"]["size"] != float64(domain.EmbeddingDimensions) || body["vectors"]["distance"] != "Cosine" ||
				body["metadata"][collectionProfileDigestKey] != supportedProfileDigestHex() {
				t.Fatalf("unexpected collection creation request: %s %#v", request.Method, body)
			}
			return response(http.StatusOK, `{}`)
		default:
			return response(http.StatusOK, `{"result":{"config":{"metadata":{"raglibrarian_index_profile_digest":"`+supportedProfileDigestHex()+`"},"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`)
		}
	})}
	store, _ := NewQdrant("http://qdrant.test", "evidence", client)
	if err := store.EnsureCollection(context.Background()); err != nil || requests != 3 {
		t.Fatalf("EnsureCollection() requests=%d error=%v", requests, err)
	}
}

func TestQdrantCheckReadyRejectsMissingProfileDigest(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
		return response(http.StatusOK, `{"result":{"config":{"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`)
	})}
	store, _ := NewQdrant("http://qdrant.test", "evidence", client)

	if err := store.CheckReady(context.Background()); err == nil {
		t.Fatal("CheckReady() accepted collection without profile digest")
	}
}

func TestQdrantCheckReadyRejectsDifferentProfileDigest(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
		return response(http.StatusOK, `{"result":{"config":{"metadata":{"raglibrarian_index_profile_digest":"different"},"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`)
	})}
	store, _ := NewQdrant("http://qdrant.test", "evidence", client)

	if err := store.CheckReady(context.Background()); err == nil {
		t.Fatal("CheckReady() accepted collection with incompatible profile digest")
	}
}

func TestQdrantStagesBeforeActivatingJob(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		requests++
		contents, _ := io.ReadAll(request.Body)
		switch requests {
		case 1:
			if !bytes.Contains(contents, []byte(`"indexed":"false"`)) || !bytes.Contains(contents, []byte(`"job_id":"job-1"`)) || !bytes.Contains(contents, []byte(`"vector_kind":"chunk"`)) {
				t.Fatalf("chunk upsert did not stage the job: %s", contents)
			}
		case 2:
			if !bytes.Contains(contents, []byte(`"indexed":"false"`)) || !bytes.Contains(contents, []byte(`"document_id":"document-1"`)) || !bytes.Contains(contents, []byte(`"vector_kind":"document"`)) {
				t.Fatalf("document upsert did not stage the job: %s", contents)
			}
		case 3:
			if !bytes.Contains(contents, []byte(`"indexed":"true"`)) || !bytes.Contains(contents, []byte(`"job_id"`)) {
				t.Fatalf("activation did not target staged job: %s", contents)
			}
		case 4:
			if !bytes.Contains(contents, []byte(`"indexed":"false"`)) || !bytes.Contains(contents, []byte(`"job_id"`)) {
				t.Fatalf("deactivation did not target staged job: %s", contents)
			}
		default:
			t.Fatalf("unexpected request %d", requests)
		}
		return response(http.StatusOK, `{}`)
	})}
	store, _ := NewQdrant("http://qdrant.test", "evidence", client)
	record := application.EvidenceRecord{Evidence: application.Evidence{EvidenceID: "book-1:chunk-1", ChunkID: "chunk-1", BookID: "book-1", Passage: "evidence"}, JobID: "job-1", Vector: make([]float32, domain.EmbeddingDimensions)}
	if err := store.UpsertChunks(context.Background(), []application.EvidenceRecord{record}); err != nil {
		t.Fatal(err)
	}
	document := application.DocumentRecord{DocumentResult: application.DocumentResult{DocumentID: "document-1", JobID: "job-1", BookID: "book-1", ChunkCount: 1}, Vector: make([]float32, domain.EmbeddingDimensions)}
	if err := store.UpsertDocument(context.Background(), document); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateJob(context.Background(), "job-1"); err != nil || requests != 3 {
		t.Fatalf("ActivateJob() requests=%d error=%v", requests, err)
	}
	if err := store.DeactivateJob(context.Background(), "job-1"); err != nil || requests != 4 {
		t.Fatalf("DeactivateJob() requests=%d error=%v", requests, err)
	}
}

type roundTripFunc func(*http.Request) *http.Response

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request), nil
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}
}
