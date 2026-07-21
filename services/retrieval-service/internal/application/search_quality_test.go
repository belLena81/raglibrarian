package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/vector"
)

func TestSearchQualityBenchmark(t *testing.T) {
	store := newQualityQdrant(t)
	searcher, err := application.NewSearcher(&qualityEmbedder{}, store, qualityVisibility{})
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "reader-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{
		Question: "How do deterministic retries remain safe?",
		Limit:    3,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	assertSearchQuality(t, result)

	empty, err := searcher.Search(context.Background(), domain.Actor{UserID: "reader-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{
		Question: "deterministic retries",
		Filters:  domain.SearchFilters{Author: "absent author"},
		Limit:    5,
	})
	if err != nil {
		t.Fatalf("filtered Search() error = %v", err)
	}
	if len(empty.Evidence) != 0 || len(empty.Documents) != 0 {
		t.Fatalf("metadata-filtered benchmark fabricated %d evidence results and %d document results", len(empty.Evidence), len(empty.Documents))
	}

	unrelated, err := searcher.Search(context.Background(), domain.Actor{UserID: "reader-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{
		Question: "unsupported topic",
		Limit:    5,
	})
	if err != nil {
		t.Fatalf("unrelated Search() error = %v", err)
	}
	if len(unrelated.Evidence) != 0 || len(unrelated.Documents) != 0 {
		t.Fatalf("unrelated benchmark fabricated %d evidence results and %d document results", len(unrelated.Evidence), len(unrelated.Documents))
	}
}

func newQualityQdrant(t *testing.T) *vector.Qdrant {
	t.Helper()
	client := &http.Client{Transport: qualityQdrantTransport{points: qualityCorpus()}}
	store, err := vector.NewQdrant("http://qdrant-quality.test", "quality", client)
	if err != nil {
		t.Fatalf("NewQdrant() error = %v", err)
	}
	return store
}

func assertSearchQuality(t *testing.T, result application.SearchResult) {
	t.Helper()
	if len(result.Evidence) == 0 {
		t.Fatal("quality benchmark returned no evidence")
	}
	evidence := result.Evidence[0]
	if evidence.EvidenceID != "evidence-retry" || evidence.ChunkID == "" || evidence.BookID != "book-retry" ||
		evidence.PageStart < 1 || evidence.PageEnd < evidence.PageStart || evidence.Score < 0.90 ||
		!strings.Contains(evidence.Passage, "Deterministic output makes retries harmless") {
		t.Fatalf("top evidence did not satisfy citation benchmark: %#v", evidence)
	}
	if len(result.Documents) == 0 {
		t.Fatal("quality benchmark returned no document-level matches")
	}
	document := result.Documents[0]
	if document.DocumentID != "document-retry" || document.BookID != "book-retry" || document.ChunkCount == 0 ||
		document.PageStart < 1 || document.PageEnd < document.PageStart || document.Score < 0.90 || len(document.Evidence) == 0 {
		t.Fatalf("top document did not satisfy document benchmark: %#v", document)
	}
	if result.Documents[0].Evidence[0].EvidenceID != "evidence-retry" {
		t.Fatalf("document evidence was not hydrated from the matching chunk: %#v", result.Documents[0].Evidence)
	}
}

type qualityEmbedder struct{}

func (*qualityEmbedder) EmbedQuery(_ context.Context, question string) ([]float32, error) {
	vector := make([]float32, domain.EmbeddingDimensions)
	switch {
	case strings.Contains(strings.ToLower(question), "unsupported"):
		vector[0] = -1
	default:
		vector[0] = 1
	}
	return vector, nil
}

type qualityVisibility struct{}

func (qualityVisibility) FilterIndexed(_ context.Context, values []application.Evidence) ([]application.Evidence, error) {
	return values, nil
}

func (qualityVisibility) FilterIndexedDocuments(_ context.Context, values []application.DocumentResult) ([]application.DocumentResult, error) {
	return values, nil
}

type qualityQdrantTransport struct {
	points []qualityPoint
}

func (transport qualityQdrantTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case "/collections/quality/points/query":
		var query qualityQueryRequest
		if err := json.NewDecoder(request.Body).Decode(&query); err != nil {
			return qualityResponse(http.StatusBadRequest, `{}`), nil
		}
		return qualityJSONResponse(qualityQueryResponse{Result: qualityQueryResult{Points: transport.search(query)}}), nil
	case "/collections/quality/points/query/batch":
		var batch qualityQueryBatchRequest
		if err := json.NewDecoder(request.Body).Decode(&batch); err != nil {
			return qualityResponse(http.StatusBadRequest, `{}`), nil
		}
		results := make([]qualityQueryResult, len(batch.Searches))
		for index, query := range batch.Searches {
			results[index] = qualityQueryResult{Points: transport.search(query)}
		}
		return qualityJSONResponse(qualityQueryBatchResponse{Result: results}), nil
	default:
		return qualityResponse(http.StatusNotFound, `{}`), nil
	}
}

func (transport qualityQdrantTransport) search(query qualityQueryRequest) []qualityScoredPoint {
	candidates := make([]qualityScoredPoint, 0, len(transport.points))
	for _, point := range transport.points {
		if !qualityMatchesFilter(point.Payload, query.Filter) {
			continue
		}
		score := qualityCosine(query.Query, point.Vector)
		if score < query.ScoreThreshold {
			continue
		}
		candidates = append(candidates, qualityScoredPoint{Score: score, Payload: point.Payload})
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		return candidates[left].Score > candidates[right].Score
	})
	if query.Offset >= len(candidates) {
		return nil
	}
	end := query.Offset + query.Limit
	if end > len(candidates) {
		end = len(candidates)
	}
	return candidates[query.Offset:end]
}

func qualityMatchesFilter(payload qualityPayload, filter *qualityFilter) bool {
	if filter == nil {
		return true
	}
	for _, condition := range filter.Must {
		switch condition.Key {
		case "indexed":
			if condition.Match == nil || payload.Indexed != condition.Match.Value {
				return false
			}
		case "vector_kind":
			if condition.Match == nil || payload.VectorKind != condition.Match.Value {
				return false
			}
		case "job_id":
			if condition.Match == nil || payload.JobID != condition.Match.Value {
				return false
			}
		case "author_normalized":
			if condition.Match == nil || payload.AuthorNormalized != condition.Match.Value {
				return false
			}
		case "tags_normalized":
			if condition.Match == nil || !qualityContains(payload.TagsNormalized, condition.Match.Value) {
				return false
			}
		case "year":
			if condition.Range != nil && condition.Range.GreaterThanOrEqual != nil && payload.Year < *condition.Range.GreaterThanOrEqual {
				return false
			}
			if condition.Range != nil && condition.Range.LessThanOrEqual != nil && payload.Year > *condition.Range.LessThanOrEqual {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func qualityContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func qualityCosine(left, right []float32) float64 {
	var dot, leftNorm, rightNorm float64
	for index := 0; index < len(left) && index < len(right); index++ {
		leftValue := float64(left[index])
		rightValue := float64(right[index])
		dot += leftValue * rightValue
		leftNorm += leftValue * leftValue
		rightNorm += rightValue * rightValue
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func qualityCorpus() []qualityPoint {
	return []qualityPoint{
		{
			Vector: qualityVector(1, 0),
			Payload: qualityPayload{
				EvidenceID:       "evidence-retry",
				ChunkID:          "chunk-retry",
				JobID:            "job-retry",
				BookID:           "book-retry",
				Title:            "Synthetic Systems",
				Author:           "RAGLibrarian QA",
				AuthorNormalized: "raglibrarian qa",
				Year:             2026,
				Tags:             []string{"m5-quality"},
				TagsNormalized:   []string{"m5-quality"},
				Chapter:          "Deterministic Retrieval",
				Section:          "Retries",
				PageStart:        10,
				PageEnd:          11,
				Passage:          "Deterministic output makes retries harmless because replayed work reaches the same manifest.",
				Indexed:          "true",
				VectorKind:       "chunk",
			},
		},
		{
			Vector: qualityVector(1, 0),
			Payload: qualityPayload{
				DocumentID:       "document-retry",
				JobID:            "job-retry",
				BookID:           "book-retry",
				Title:            "Synthetic Systems",
				Author:           "RAGLibrarian QA",
				AuthorNormalized: "raglibrarian qa",
				Year:             2026,
				Tags:             []string{"m5-quality"},
				TagsNormalized:   []string{"m5-quality"},
				PageStart:        1,
				PageEnd:          42,
				ChunkCount:       12,
				Indexed:          "true",
				VectorKind:       "document",
			},
		},
		{
			Vector: qualityVector(0, 1),
			Payload: qualityPayload{
				EvidenceID:       "evidence-queue",
				ChunkID:          "chunk-queue",
				JobID:            "job-queue",
				BookID:           "book-queue",
				Title:            "Queue Operations",
				Author:           "RAGLibrarian QA",
				AuthorNormalized: "raglibrarian qa",
				Year:             2026,
				Tags:             []string{"operations"},
				TagsNormalized:   []string{"operations"},
				Chapter:          "Queue Depth",
				Section:          "Monitoring",
				PageStart:        3,
				PageEnd:          4,
				Passage:          "Queue depth is monitored separately from search quality.",
				Indexed:          "true",
				VectorKind:       "chunk",
			},
		},
		{
			Vector: qualityVector(1, 0),
			Payload: qualityPayload{
				EvidenceID:       "evidence-filtered",
				ChunkID:          "chunk-filtered",
				JobID:            "job-filtered",
				BookID:           "book-filtered",
				Title:            "Filtered Systems",
				Author:           "Other Author",
				AuthorNormalized: "other author",
				Year:             2026,
				Tags:             []string{"m5-quality"},
				TagsNormalized:   []string{"m5-quality"},
				Chapter:          "Filters",
				Section:          "Authors",
				PageStart:        7,
				PageEnd:          8,
				Passage:          "This high scoring candidate must disappear when metadata filters do not match.",
				Indexed:          "true",
				VectorKind:       "chunk",
			},
		},
	}
}

func qualityVector(first, second float32) []float32 {
	vector := make([]float32, domain.EmbeddingDimensions)
	vector[0] = first
	vector[1] = second
	return vector
}

func qualityJSONResponse(value any) *http.Response {
	body, _ := json.Marshal(value)
	return qualityResponse(http.StatusOK, string(body))
}

func qualityResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

type qualityQueryRequest struct {
	Query          []float32      `json:"query"`
	Limit          int            `json:"limit"`
	Offset         int            `json:"offset,omitempty"`
	Filter         *qualityFilter `json:"filter,omitempty"`
	ScoreThreshold float64        `json:"score_threshold"`
}

type qualityQueryBatchRequest struct {
	Searches []qualityQueryRequest `json:"searches"`
}

type qualityFilter struct {
	Must []qualityCondition `json:"must"`
}

type qualityCondition struct {
	Key   string             `json:"key"`
	Match *qualityMatchValue `json:"match,omitempty"`
	Range *qualityRangeValue `json:"range,omitempty"`
}

type qualityMatchValue struct {
	Value string `json:"value"`
}

type qualityRangeValue struct {
	GreaterThanOrEqual *int `json:"gte,omitempty"`
	LessThanOrEqual    *int `json:"lte,omitempty"`
}

type qualityQueryResponse struct {
	Result qualityQueryResult `json:"result"`
}

type qualityQueryBatchResponse struct {
	Result []qualityQueryResult `json:"result"`
}

type qualityQueryResult struct {
	Points []qualityScoredPoint `json:"points"`
}

type qualityPoint struct {
	Vector  []float32
	Payload qualityPayload
}

type qualityScoredPoint struct {
	Score   float64        `json:"score"`
	Payload qualityPayload `json:"payload"`
}

type qualityPayload struct {
	EvidenceID       string   `json:"evidence_id,omitempty"`
	ChunkID          string   `json:"chunk_id,omitempty"`
	DocumentID       string   `json:"document_id,omitempty"`
	JobID            string   `json:"job_id,omitempty"`
	BookID           string   `json:"book_id,omitempty"`
	Title            string   `json:"title,omitempty"`
	Author           string   `json:"author,omitempty"`
	AuthorNormalized string   `json:"author_normalized,omitempty"`
	Year             int      `json:"year,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	TagsNormalized   []string `json:"tags_normalized,omitempty"`
	Chapter          string   `json:"chapter,omitempty"`
	Section          string   `json:"section,omitempty"`
	PageStart        uint32   `json:"page_start,omitempty"`
	PageEnd          uint32   `json:"page_end,omitempty"`
	Passage          string   `json:"passage,omitempty"`
	ChunkCount       uint32   `json:"chunk_count,omitempty"`
	Indexed          string   `json:"indexed,omitempty"`
	VectorKind       string   `json:"vector_kind,omitempty"`
}
