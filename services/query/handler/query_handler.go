// Package handler wires the HTTP layer to the QueryUseCase.
// It owns: JSON request/response DTOs, input decoding, error-to-status mapping.
// It must not contain business logic — that lives in the use case.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/query/usecase"
)

// ── Request / Response DTOs ──────────────────────────────────────────────────
// DTOs are intentionally separate from domain structs.  They carry JSON tags
// and can evolve (e.g. add deprecated aliases) without polluting the domain.

// QueryRequest is the JSON body accepted by POST /query.
type QueryRequest struct {
	Question string `json:"question"`
	UserID   string `json:"user_id"`
}

// BookDTO is the nested book representation in a query response.
type BookDTO struct {
	Title  string `json:"title"`
	Author string `json:"author"`
	Year   int    `json:"year"`
}

// SearchResultDTO is one ranked answer in the query response.
type SearchResultDTO struct {
	Book    BookDTO `json:"book"`
	Chapter string  `json:"chapter"`
	Pages   []int   `json:"pages"`
	Passage string  `json:"passage"`
	Score   float64 `json:"score"`
}

// QueryResponse is the top-level JSON response for POST /query.
// The shape is locked from Iteration 1 onward; later iterations only fill in
// real data — they never change this structure.
type QueryResponse struct {
	Query   string            `json:"query"`
	Results []SearchResultDTO `json:"results"`
}

// errorResponse is the consistent error envelope for all 4xx/5xx replies.
type errorResponse struct {
	Error string `json:"error"`
}

// ── Handler ──────────────────────────────────────────────────────────────────

// QueryHandler handles HTTP requests for the query service.
type QueryHandler struct {
	uc usecase.QueryUseCase
}

// NewQueryHandler constructs a QueryHandler with the given use case.
func NewQueryHandler(uc usecase.QueryUseCase) *QueryHandler {
	if uc == nil {
		panic("handler: QueryUseCase must not be nil")
	}
	return &QueryHandler{uc: uc}
}

// Query handles POST /query.
// It decodes the request, delegates to the use case, then encodes the response.
func (h *QueryHandler) Query(w http.ResponseWriter, r *http.Request) {
	var req QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	results, err := h.uc.Answer(r.Context(), req.UserID, req.Question)
	if err != nil {
		status := domainErrToStatus(err)
		writeError(w, status, err.Error())
		return
	}

	resp := QueryResponse{
		Query:   req.Question,
		Results: toSearchResultDTOs(results),
	}

	writeJSON(w, http.StatusOK, resp)
}

// Health handles GET /healthz.
// Returns 200 OK with a simple JSON body so load-balancers and k8s probes work.
func (h *QueryHandler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// domainErrToStatus maps well-known domain errors to appropriate HTTP codes.
// Unmapped errors are treated as internal server errors.
func domainErrToStatus(err error) int {
	switch {
	case errors.Is(err, domain.ErrEmptyQuestion),
		errors.Is(err, domain.ErrEmptyUserId):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

func toSearchResultDTOs(results []domain.SearchResult) []SearchResultDTO {
	dtos := make([]SearchResultDTO, 0, len(results))
	for _, r := range results {
		b := r.Book()
		dtos = append(dtos, SearchResultDTO{
			Book: BookDTO{
				Title:  b.Title(),
				Author: b.Author(),
				Year:   b.Year(),
			},
			Chapter: r.Chapter(),
			Pages:   r.Pages(),
			Passage: r.Passage(),
			Score:   r.Score(),
		})
	}
	return dtos
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}
