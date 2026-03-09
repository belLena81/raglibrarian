package query_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/query"
	"github.com/belLena81/raglibrarian/services/query/handler"
)

// fakeUseCase is a local test double — it avoids importing the usecase package,
// keeping the test focused on routing concerns only.
type fakeUseCase struct {
	results []domain.SearchResult
}

func (f *fakeUseCase) Answer(_ context.Context, _, _ string) ([]domain.SearchResult, error) {
	return f.results, nil
}

func newTestRouter(t *testing.T) http.Handler {
	t.Helper()
	log := zaptest.NewLogger(t)
	uc := &fakeUseCase{results: []domain.SearchResult{}}
	qh := handler.NewQueryHandler(uc, log)
	return query.NewRouter(qh, log)
}

func TestRouter_POST_Query_Returns200(t *testing.T) {
	router := newTestRouter(t)

	body, _ := json.Marshal(map[string]string{
		"question": "What is a goroutine?",
		"user_id":  "user-abc",
	})

	req := httptest.NewRequest(http.MethodPost, "/query/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRouter_GET_Healthz_Returns200(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRouter_UnknownRoute_Returns404(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRouter_MethodNotAllowed_Returns405(t *testing.T) {
	router := newTestRouter(t)

	// GET on a POST-only route
	req := httptest.NewRequest(http.MethodGet, "/query/", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestRouter_RequestID_IsInjected(t *testing.T) {
	router := newTestRouter(t)

	body, _ := json.Marshal(map[string]string{
		"question": "Any question?",
		"user_id":  "u1",
	})

	req := httptest.NewRequest(http.MethodPost, "/query/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	// chi's RequestID middleware injects X-Request-Id into the response
	assert.NotEmpty(t, rr.Header().Get("X-Request-Id"))
}

func TestRouter_Panic_Returns500(t *testing.T) {
	// Wire a use case that panics to verify Recoverer middleware works
	log := zaptest.NewLogger(t)
	panicUC := &panicUseCase{}
	qh := handler.NewQueryHandler(panicUC, log)
	router := query.NewRouter(qh, log)

	body, _ := json.Marshal(map[string]string{
		"question": "trigger panic",
		"user_id":  "u1",
	})

	req := httptest.NewRequest(http.MethodPost, "/query/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

type panicUseCase struct{}

func (p *panicUseCase) Answer(_ context.Context, _, _ string) ([]domain.SearchResult, error) {
	panic("unexpected panic in use case")
}

// TestRouter_StubWiring is an end-to-end smoke test using the real stub
// repository to confirm the default wiring works before any real infra exists.
func TestRouter_StubWiring_ReturnsStructuredPayload(t *testing.T) {
	// Build with real stub repository to prove the full wiring
	stubRepo := stubRepository()
	uc := &fakeUseCase{results: stubRepo}
	qh := handler.NewQueryHandler(uc)
	router := query.NewRouter(qh)

	body, _ := json.Marshal(map[string]string{
		"question": "Which book explains goroutine scheduling?",
		"user_id":  "user-abc",
	})

	req := httptest.NewRequest(http.MethodPost, "/query/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp handler.QueryResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))

	assert.Equal(t, "Which book explains goroutine scheduling?", resp.Query)
	assert.NotEmpty(t, resp.Results)
	// Every result must have the mandatory fields populated
	for _, r := range resp.Results {
		assert.NotEmpty(t, r.Book.Title)
		assert.NotEmpty(t, r.Chapter)
		assert.NotEmpty(t, r.Passage)
		assert.NotEmpty(t, r.Pages)
		assert.Greater(t, r.Score, 0.0)
	}
}

func stubRepository() []domain.SearchResult {
	goBook, _ := domain.NewBook("The Go Programming Language", "Donovan & Kernighan", 2015)
	result, _ := domain.NewSearchResult(
		"test-qid",
		goBook,
		"Chapter 9 — Concurrency",
		"Goroutines are multiplexed onto OS threads.",
		[]int{217, 218},
		0.94,
	)
	return []domain.SearchResult{result}
}
