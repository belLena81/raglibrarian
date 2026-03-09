package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/query/handler"
)

// ── Fake Use Case ─────────────────────────────────────────────────────────────
// Plain struct fake — avoids mock generation overhead for a single-method interface.

type fakeQueryUseCase struct {
	results []domain.SearchResult
	err     error
}

func (f *fakeQueryUseCase) Answer(_ context.Context, _, _ string) ([]domain.SearchResult, error) {
	return f.results, f.err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func doPost(t *testing.T, h *handler.QueryHandler, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	h.Query(rr, req)
	return rr
}

func decodeQueryResponse(t *testing.T, rr *httptest.ResponseRecorder) handler.QueryResponse {
	t.Helper()
	var resp handler.QueryResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	return resp
}

func makeResult(t *testing.T) domain.SearchResult {
	t.Helper()
	book, err := domain.NewBook("The Go Programming Language", "Donovan & Kernighan", 2015)
	require.NoError(t, err)
	result, err := domain.NewSearchResult(
		"qid-001",
		book,
		"Chapter 9 — Concurrency",
		"Goroutines are multiplexed onto OS threads.",
		[]int{217, 218},
		0.94,
	)
	require.NoError(t, err)
	return result
}

// ── POST /query tests ──────────────────────────────────────────────────────────

func TestQueryHandler_Query_Returns200_WithResults(t *testing.T) {
	result := makeResult(t)
	uc := &fakeQueryUseCase{results: []domain.SearchResult{result}}
	h := handler.NewQueryHandler(uc, zaptest.NewLogger(t))

	rr := doPost(t, h, map[string]string{
		"question": "What is a goroutine?",
		"user_id":  "user-123",
	})

	assert.Equal(t, http.StatusOK, rr.Code)

	resp := decodeQueryResponse(t, rr)
	assert.Equal(t, "What is a goroutine?", resp.Query)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "The Go Programming Language", resp.Results[0].Book.Title)
	assert.Equal(t, "Donovan & Kernighan", resp.Results[0].Book.Author)
	assert.Equal(t, 2015, resp.Results[0].Book.Year)
	assert.Equal(t, "Chapter 9 — Concurrency", resp.Results[0].Chapter)
	assert.Equal(t, []int{217, 218}, resp.Results[0].Pages)
	assert.Equal(t, "Goroutines are multiplexed onto OS threads.", resp.Results[0].Passage)
	assert.InDelta(t, 0.94, resp.Results[0].Score, 0.0001)
}

func TestQueryHandler_Query_Returns200_EmptyResults(t *testing.T) {
	uc := &fakeQueryUseCase{results: []domain.SearchResult{}}
	h := handler.NewQueryHandler(uc, zaptest.NewLogger(t))

	rr := doPost(t, h, map[string]string{
		"question": "Something obscure?",
		"user_id":  "user-123",
	})

	assert.Equal(t, http.StatusOK, rr.Code)

	resp := decodeQueryResponse(t, rr)
	assert.Empty(t, resp.Results)
}

func TestQueryHandler_Query_Returns422_OnDomainValidationError(t *testing.T) {
	tests := []struct {
		name string
		body map[string]string
	}{
		{
			name: "empty question",
			body: map[string]string{"question": "", "user_id": "user-123"},
		},
		{
			name: "whitespace question",
			body: map[string]string{"question": "   ", "user_id": "user-123"},
		},
		{
			name: "empty user id",
			body: map[string]string{"question": "What is a goroutine?", "user_id": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use case returns a domain error (simulating what QueryService would return)
			uc := &fakeQueryUseCase{err: fmt.Errorf("invalid query: %w", domain.ErrEmptyQuestion)}
			if tt.body["user_id"] == "" {
				uc.err = fmt.Errorf("invalid query: %w", domain.ErrEmptyUserId)
			}
			h := handler.NewQueryHandler(uc, zaptest.NewLogger(t))

			rr := doPost(t, h, tt.body)
			assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
		})
	}
}

func TestQueryHandler_Query_Returns500_OnInternalError(t *testing.T) {
	uc := &fakeQueryUseCase{err: errors.New("qdrant: connection refused")}
	h := handler.NewQueryHandler(uc, zaptest.NewLogger(t))

	rr := doPost(t, h, map[string]string{
		"question": "What is a goroutine?",
		"user_id":  "user-123",
	})

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestQueryHandler_Query_Returns400_OnInvalidJSON(t *testing.T) {
	uc := &fakeQueryUseCase{}
	h := handler.NewQueryHandler(uc, zaptest.NewLogger(t))

	req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString("{invalid json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.Query(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestQueryHandler_Query_ResponseHasJSONContentType(t *testing.T) {
	uc := &fakeQueryUseCase{results: []domain.SearchResult{}}
	h := handler.NewQueryHandler(uc, zaptest.NewLogger(t))

	rr := doPost(t, h, map[string]string{"question": "test?", "user_id": "u"})

	assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")
}

func TestQueryHandler_Query_MultipleResults_PreservesOrder(t *testing.T) {
	book1, _ := domain.NewBook("Book Alpha", "Author A", 2020)
	book2, _ := domain.NewBook("Book Beta", "Author B", 2021)
	r1, _ := domain.NewSearchResult("qid", book1, "Ch1", "first passage", []int{1}, 0.95)
	r2, _ := domain.NewSearchResult("qid", book2, "Ch2", "second passage", []int{2}, 0.80)

	uc := &fakeQueryUseCase{results: []domain.SearchResult{r1, r2}}
	h := handler.NewQueryHandler(uc, zaptest.NewLogger(t))

	rr := doPost(t, h, map[string]string{"question": "test?", "user_id": "u"})

	resp := decodeQueryResponse(t, rr)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, "Book Alpha", resp.Results[0].Book.Title)
	assert.Equal(t, "Book Beta", resp.Results[1].Book.Title)
}

// ── GET /healthz tests ────────────────────────────────────────────────────────

func TestQueryHandler_Health_Returns200(t *testing.T) {
	uc := &fakeQueryUseCase{}
	h := handler.NewQueryHandler(uc, zaptest.NewLogger(t))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h.Health(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestQueryHandler_Health_ResponseBody(t *testing.T) {
	uc := &fakeQueryUseCase{}
	h := handler.NewQueryHandler(uc, zaptest.NewLogger(t))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h.Health(rr, req)

	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "ok", body["status"])
}

// ── Wiring guards ─────────────────────────────────────────────────────────────

func TestNewQueryHandler_NilUseCase_Panics(t *testing.T) {
	assert.Panics(t, func() {
		handler.NewQueryHandler(nil, zaptest.NewLogger(t))
	})
}

func TestNewQueryHandler_NilLogger_Panics(t *testing.T) {
	uc := &fakeQueryUseCase{}
	assert.Panics(t, func() {
		handler.NewQueryHandler(uc, nil)
	})
}
