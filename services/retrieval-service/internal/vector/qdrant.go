// Package vector implements Retrieval's private Qdrant adapter.
package vector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

const (
	maximumQdrantResponseBytes = 4 << 20
	collectionProfileDigestKey = "raglibrarian_index_profile_digest"
)

type Qdrant struct {
	endpoint   string
	collection string
	apiKey     string
	client     *http.Client
}

func NewQdrant(endpoint, collection string, client *http.Client) (*Qdrant, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || collection == "" || strings.ContainsAny(collection, "/?#") || client == nil {
		return nil, errors.New("invalid Qdrant configuration")
	}
	return &Qdrant{endpoint: strings.TrimRight(endpoint, "/"), collection: collection, client: client}, nil
}

func NewAuthenticatedQdrant(endpoint, collection, apiKey string, client *http.Client) (*Qdrant, error) {
	qdrant, err := NewQdrant(endpoint, collection, client)
	if err != nil || strings.TrimSpace(apiKey) == "" || strings.ContainsAny(apiKey, "\r\n") {
		return nil, errors.New("invalid Qdrant configuration")
	}
	qdrant.apiKey = apiKey
	return qdrant, nil
}

type queryRequest struct {
	Query          []float32 `json:"query"`
	Limit          int       `json:"limit"`
	Offset         int       `json:"offset,omitempty"`
	WithPayload    bool      `json:"with_payload"`
	Filter         *filter   `json:"filter,omitempty"`
	ScoreThreshold float64   `json:"score_threshold"`
}

type filter struct {
	Must []condition `json:"must"`
}

type condition struct {
	Key   string      `json:"key"`
	Match *matchValue `json:"match,omitempty"`
	Range *rangeValue `json:"range,omitempty"`
}

type matchValue struct {
	Value string `json:"value"`
}

type rangeValue struct {
	GreaterThanOrEqual *int `json:"gte,omitempty"`
	LessThanOrEqual    *int `json:"lte,omitempty"`
}

type queryResponse struct {
	Result queryResult `json:"result"`
}

type queryBatchRequest struct {
	Searches []queryRequest `json:"searches"`
}

type queryBatchResponse struct {
	Result []queryResult `json:"result"`
}

type queryResult struct {
	Points []queryPoint `json:"points"`
}

type queryPoint struct {
	Score   float64      `json:"score"`
	Payload pointPayload `json:"payload"`
}

type pointPayload struct {
	EvidenceID string   `json:"evidence_id"`
	ChunkID    string   `json:"chunk_id"`
	DocumentID string   `json:"document_id"`
	JobID      string   `json:"job_id"`
	BookID     string   `json:"book_id"`
	Title      string   `json:"title"`
	Author     string   `json:"author"`
	Year       int      `json:"year"`
	Tags       []string `json:"tags"`
	Chapter    string   `json:"chapter"`
	Section    string   `json:"section"`
	PageStart  uint32   `json:"page_start"`
	PageEnd    uint32   `json:"page_end"`
	Passage    string   `json:"passage"`
	ChunkCount uint32   `json:"chunk_count"`
}

func (q *Qdrant) Search(ctx context.Context, query domain.SearchQuery, vector []float32, limit, offset int) ([]application.Evidence, error) {
	return q.searchEvidence(ctx, query, vector, limit, offset, []condition{{Key: "vector_kind", Match: &matchValue{Value: "chunk"}}})
}

func (q *Qdrant) SearchDocuments(ctx context.Context, query domain.SearchQuery, vector []float32, limit, offset int) ([]application.DocumentResult, error) {
	payload, err := json.Marshal(queryRequest{Query: vector, Limit: limit, Offset: offset, WithPayload: true, Filter: buildFilter(query.Filters(), []condition{{Key: "vector_kind", Match: &matchValue{Value: "document"}}}), ScoreThreshold: 0.25})
	if err != nil {
		return nil, errors.New("encode vector query")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, q.endpoint+"/collections/"+q.collection+"/points/query", bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("create vector query")
	}
	request.Header.Set("Content-Type", "application/json")
	if q.apiKey != "" {
		request.Header.Set("api-key", q.apiKey)
	}
	response, err := q.client.Do(request) // #nosec G704 -- NewQdrant accepts only a validated operator-controlled endpoint.
	if err != nil {
		return nil, errors.New("vector dependency unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumQdrantResponseBytes))
		return nil, errors.New("vector dependency rejected query")
	}
	var decoded queryResponse
	if err = json.NewDecoder(io.LimitReader(response.Body, maximumQdrantResponseBytes)).Decode(&decoded); err != nil {
		return nil, errors.New("invalid vector response")
	}
	results := make([]application.DocumentResult, 0, len(decoded.Result.Points))
	jobIDs := make([]string, 0, len(decoded.Result.Points))
	for _, point := range decoded.Result.Points {
		value := point.Payload
		if value.DocumentID == "" || value.JobID == "" || value.BookID == "" || value.ChunkCount == 0 {
			continue
		}
		results = append(results, application.DocumentResult{DocumentID: value.DocumentID, JobID: value.JobID, BookID: value.BookID,
			Title: value.Title, Author: value.Author, Year: value.Year, Tags: value.Tags, ChunkCount: value.ChunkCount,
			PageStart: value.PageStart, PageEnd: value.PageEnd, Score: point.Score})
		jobIDs = append(jobIDs, value.JobID)
	}
	evidence, err := q.searchEvidenceBatch(ctx, query, vector, jobIDs, 3)
	if err != nil {
		return nil, err
	}
	hydrated := make([]application.DocumentResult, 0, len(results))
	for index, result := range results {
		if len(evidence[index]) == 0 {
			continue
		}
		result.Evidence = evidence[index]
		hydrated = append(hydrated, result)
	}
	return hydrated, nil
}

func (q *Qdrant) searchEvidence(ctx context.Context, query domain.SearchQuery, vector []float32, limit, offset int, extra []condition) ([]application.Evidence, error) {
	payload, err := json.Marshal(queryRequest{Query: vector, Limit: limit, Offset: offset, WithPayload: true, Filter: buildFilter(query.Filters(), extra), ScoreThreshold: 0.25})
	if err != nil {
		return nil, errors.New("encode vector query")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, q.endpoint+"/collections/"+q.collection+"/points/query", bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("create vector query")
	}
	request.Header.Set("Content-Type", "application/json")
	if q.apiKey != "" {
		request.Header.Set("api-key", q.apiKey)
	}
	response, err := q.client.Do(request) // #nosec G704 -- NewQdrant accepts only a validated operator-controlled endpoint.
	if err != nil {
		return nil, errors.New("vector dependency unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumQdrantResponseBytes))
		return nil, errors.New("vector dependency rejected query")
	}
	var decoded queryResponse
	if err = json.NewDecoder(io.LimitReader(response.Body, maximumQdrantResponseBytes)).Decode(&decoded); err != nil {
		return nil, errors.New("invalid vector response")
	}
	results := make([]application.Evidence, 0, len(decoded.Result.Points))
	for _, point := range decoded.Result.Points {
		value := point.Payload
		if value.EvidenceID == "" || value.BookID == "" || value.Passage == "" {
			continue
		}
		results = append(results, application.Evidence{EvidenceID: value.EvidenceID, ChunkID: value.ChunkID, JobID: value.JobID, BookID: value.BookID,
			Title: value.Title, Author: value.Author, Year: value.Year, Tags: value.Tags, Chapter: value.Chapter,
			Section: value.Section, PageStart: value.PageStart, PageEnd: value.PageEnd, Passage: value.Passage, Score: point.Score})
	}
	return results, nil
}

func (q *Qdrant) searchEvidenceBatch(ctx context.Context, query domain.SearchQuery, vector []float32, jobIDs []string, limit int) ([][]application.Evidence, error) {
	results := make([][]application.Evidence, len(jobIDs))
	if len(jobIDs) == 0 {
		return results, nil
	}
	searches := make([]queryRequest, len(jobIDs))
	for index, jobID := range jobIDs {
		searches[index] = queryRequest{Query: vector, Limit: limit, WithPayload: true, Filter: buildFilter(query.Filters(), []condition{
			{Key: "vector_kind", Match: &matchValue{Value: "chunk"}},
			{Key: "job_id", Match: &matchValue{Value: jobID}},
		}), ScoreThreshold: 0.25}
	}
	payload, err := json.Marshal(queryBatchRequest{Searches: searches})
	if err != nil {
		return nil, errors.New("encode vector query")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, q.endpoint+"/collections/"+q.collection+"/points/query/batch", bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("create vector query")
	}
	request.Header.Set("Content-Type", "application/json")
	if q.apiKey != "" {
		request.Header.Set("api-key", q.apiKey)
	}
	response, err := q.client.Do(request) // #nosec G704 -- NewQdrant accepts only a validated operator-controlled endpoint.
	if err != nil {
		return nil, errors.New("vector dependency unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumQdrantResponseBytes))
		return nil, errors.New("vector dependency rejected query")
	}
	var decoded queryBatchResponse
	if err = json.NewDecoder(io.LimitReader(response.Body, maximumQdrantResponseBytes)).Decode(&decoded); err != nil || len(decoded.Result) != len(jobIDs) {
		return nil, errors.New("invalid vector response")
	}
	for index, result := range decoded.Result {
		results[index] = evidenceFromPoints(result.Points)
	}
	return results, nil
}

func evidenceFromPoints(points []queryPoint) []application.Evidence {
	results := make([]application.Evidence, 0, len(points))
	for _, point := range points {
		value := point.Payload
		if value.EvidenceID == "" || value.BookID == "" || value.Passage == "" {
			continue
		}
		results = append(results, application.Evidence{EvidenceID: value.EvidenceID, ChunkID: value.ChunkID, JobID: value.JobID, BookID: value.BookID,
			Title: value.Title, Author: value.Author, Year: value.Year, Tags: value.Tags, Chapter: value.Chapter,
			Section: value.Section, PageStart: value.PageStart, PageEnd: value.PageEnd, Passage: value.Passage, Score: point.Score})
	}
	return results
}

func (q *Qdrant) CheckReady(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, q.endpoint+"/collections/"+q.collection, nil)
	if err != nil {
		return errors.New("create vector readiness request")
	}
	if q.apiKey != "" {
		request.Header.Set("api-key", q.apiKey)
	}
	response, err := q.client.Do(request) // #nosec G704 -- NewQdrant accepts only a validated operator-controlled endpoint.
	if err != nil {
		return errors.New("vector dependency unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.New("vector dependency unavailable")
	}
	var description struct {
		Result struct {
			Config struct {
				Metadata map[string]any `json:"metadata"`
				Params   struct {
					Vectors struct {
						Size     int    `json:"size"`
						Distance string `json:"distance"`
					} `json:"vectors"`
				} `json:"params"`
			} `json:"config"`
		} `json:"result"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, maximumQdrantResponseBytes)).Decode(&description); err != nil {
		return errors.New("incompatible vector collection")
	}
	profileDigest, ok := description.Result.Config.Metadata[collectionProfileDigestKey].(string)
	if description.Result.Config.Params.Vectors.Size != domain.EmbeddingDimensions || !strings.EqualFold(description.Result.Config.Params.Vectors.Distance, "cosine") ||
		!ok || profileDigest != supportedProfileDigestHex() {
		return errors.New("incompatible vector collection")
	}
	return nil
}

func (q *Qdrant) EnsureCollection(ctx context.Context) error {
	if err := q.CheckReady(ctx); err == nil {
		return nil
	}
	body, _ := json.Marshal(map[string]any{
		"vectors": map[string]any{"size": domain.EmbeddingDimensions, "distance": "Cosine"},
		"metadata": map[string]string{
			collectionProfileDigestKey: supportedProfileDigestHex(),
		},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, q.endpoint+"/collections/"+q.collection, bytes.NewReader(body))
	if err != nil {
		return errors.New("create collection request")
	}
	request.Header.Set("Content-Type", "application/json")
	if q.apiKey != "" {
		request.Header.Set("api-key", q.apiKey)
	}
	response, err := q.client.Do(request) // #nosec G704 -- NewQdrant accepts only a validated operator-controlled endpoint.
	if err != nil {
		return errors.New("vector dependency unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumQdrantResponseBytes))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return errors.New("create vector collection")
	}
	return q.CheckReady(ctx)
}

func buildFilter(filters domain.SearchFilters, extra []condition) *filter {
	conditions := make([]condition, 0, len(filters.Tags)+3+len(extra))
	conditions = append(conditions, condition{Key: "indexed", Match: &matchValue{Value: "true"}})
	conditions = append(conditions, extra...)
	if filters.Author != "" {
		conditions = append(conditions, condition{Key: "author_normalized", Match: &matchValue{Value: filters.Author}})
	}
	for _, tag := range filters.Tags {
		conditions = append(conditions, condition{Key: "tags_normalized", Match: &matchValue{Value: tag}})
	}
	if filters.YearFrom != nil || filters.YearTo != nil {
		yearRange := &rangeValue{}
		if filters.YearFrom != nil {
			yearRange.GreaterThanOrEqual = filters.YearFrom
		}
		if filters.YearTo != nil {
			yearRange.LessThanOrEqual = filters.YearTo
		}
		conditions = append(conditions, condition{Key: "year", Range: yearRange})
	}
	return &filter{Must: conditions}
}

type upsertRequest struct {
	Points []upsertPoint `json:"points"`
}

type upsertPoint struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

func (q *Qdrant) UpsertChunks(ctx context.Context, records []application.EvidenceRecord) error {
	if len(records) == 0 || len(records) > 256 {
		return errors.New("invalid vector batch")
	}
	points := make([]upsertPoint, len(records))
	for index, record := range records {
		if record.EvidenceID == "" || len(record.Vector) != domain.EmbeddingDimensions {
			return errors.New("invalid vector record")
		}
		points[index] = upsertPoint{ID: deterministicPointID(record.EvidenceID), Vector: record.Vector, Payload: map[string]any{
			"evidence_id": record.EvidenceID, "chunk_id": record.ChunkID, "book_id": record.BookID, "title": record.Title,
			"author": record.Author, "author_normalized": strings.ToLower(strings.Join(strings.Fields(record.Author), " ")), "year": record.Year,
			"tags": record.Tags, "tags_normalized": normalizedValues(record.Tags), "chapter": record.Chapter, "section": record.Section,
			"page_start": record.PageStart, "page_end": record.PageEnd, "passage": record.Passage, "job_id": record.JobID, "indexed": "false", "vector_kind": "chunk",
		}}
	}
	return q.upsertPoints(ctx, points)
}

func (q *Qdrant) UpsertDocument(ctx context.Context, record application.DocumentRecord) error {
	if record.DocumentID == "" || record.JobID == "" || record.BookID == "" || record.ChunkCount == 0 || len(record.Vector) != domain.EmbeddingDimensions {
		return errors.New("invalid document vector")
	}
	point := upsertPoint{ID: deterministicPointID("document:" + record.DocumentID), Vector: record.Vector, Payload: map[string]any{
		"document_id": record.DocumentID, "book_id": record.BookID, "title": record.Title, "author": record.Author,
		"author_normalized": strings.ToLower(strings.Join(strings.Fields(record.Author), " ")), "year": record.Year,
		"tags": record.Tags, "tags_normalized": normalizedValues(record.Tags), "page_start": record.PageStart, "page_end": record.PageEnd,
		"chunk_count": record.ChunkCount, "job_id": record.JobID, "indexed": "false", "vector_kind": "document",
	}}
	return q.upsertPoints(ctx, []upsertPoint{point})
}

func (q *Qdrant) upsertPoints(ctx context.Context, points []upsertPoint) error {
	body, err := json.Marshal(upsertRequest{Points: points})
	if err != nil {
		return errors.New("encode vector batch")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, q.endpoint+"/collections/"+q.collection+"/points?wait=true", bytes.NewReader(body))
	if err != nil {
		return errors.New("create vector upsert")
	}
	request.Header.Set("Content-Type", "application/json")
	if q.apiKey != "" {
		request.Header.Set("api-key", q.apiKey)
	}
	response, err := q.client.Do(request) // #nosec G704 -- NewQdrant accepts only a validated operator-controlled endpoint.
	if err != nil {
		return errors.New("vector dependency unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumQdrantResponseBytes))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return errors.New("vector dependency rejected upsert")
	}
	return nil
}

func (q *Qdrant) ActivateJob(ctx context.Context, jobID string) error {
	return q.setJobVisibility(ctx, jobID, "true")
}

// DeactivateJob removes a failed job from Qdrant query visibility without deleting
// its staged points, making a later idempotent replay safe.
func (q *Qdrant) DeactivateJob(ctx context.Context, jobID string) error {
	return q.setJobVisibility(ctx, jobID, "false")
}

func (q *Qdrant) setJobVisibility(ctx context.Context, jobID, indexed string) error {
	if jobID == "" {
		return errors.New("invalid index job")
	}
	body, err := json.Marshal(map[string]any{"payload": map[string]string{"indexed": indexed}, "filter": filter{Must: []condition{{Key: "job_id", Match: &matchValue{Value: jobID}}}}})
	if err != nil {
		return errors.New("encode vector visibility")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, q.endpoint+"/collections/"+q.collection+"/points/payload?wait=true", bytes.NewReader(body))
	if err != nil {
		return errors.New("create vector visibility")
	}
	request.Header.Set("Content-Type", "application/json")
	if q.apiKey != "" {
		request.Header.Set("api-key", q.apiKey)
	}
	response, err := q.client.Do(request) // #nosec G704 -- NewQdrant accepts only a validated operator-controlled endpoint.
	if err != nil {
		return errors.New("vector dependency unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumQdrantResponseBytes))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return errors.New("vector dependency rejected visibility")
	}
	return nil
}

func deterministicPointID(value string) string {
	digest := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(digest[:16])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func supportedProfileDigestHex() string {
	digest := domain.SupportedIndexProfile().Digest
	return hex.EncodeToString(digest[:])
}

func normalizedValues(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.ToLower(strings.Join(strings.Fields(value), " "))
	}
	return result
}
