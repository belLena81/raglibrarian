package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/query/usecase"
)

// ── Request / Response DTOs ───────────────────────────────────────────────────

// QueryRequest is the JSON body for POST /query.
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

// SearchResultDTO is one ranked answer in a query response.
type SearchResultDTO struct {
	Book    BookDTO `json:"book"`
	Chapter string  `json:"chapter"`
	Pages   []int   `json:"pages"`
	Passage string  `json:"passage"`
	Score   float64 `json:"score"`
}

// QueryResponse is the top-level JSON response for POST /query.
type QueryResponse struct {
	Query   string            `json:"query"`
	Results []SearchResultDTO `json:"results"`
}

// errorResponse is the error envelope for all 4xx/5xx replies.
type errorResponse struct {
	Error string `json:"error"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

// QueryHandler handles HTTP requests for the query service.
type QueryHandler struct {
	uc  usecase.QueryUseCase
	log *zap.Logger
}

// NewQueryHandler constructs a QueryHandler. Panics on nil deps.
func NewQueryHandler(uc usecase.QueryUseCase, log *zap.Logger) *QueryHandler {
	if uc == nil {
		panic("handler: QueryUseCase must not be nil")
	}
	if log == nil {
		panic("handler: Logger must not be nil")
	}
	return &QueryHandler{uc: uc, log: log}
}

// Query handles POST /query.
func (h *QueryHandler) Query(w http.ResponseWriter, r *http.Request) {
	reqLog := h.log.With(zap.String("request_id", middleware.GetReqID(r.Context())))

	var req QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		reqLog.Warn("failed to decode request body", zap.Error(err))
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	results, err := h.uc.Answer(r.Context(), req.UserID, req.Question)
	if err != nil {
		status := domainErrToStatus(err)
		if status >= http.StatusInternalServerError {
			reqLog.Error("query use case returned unexpected error",
				zap.Error(err),
				zap.String("user_id", req.UserID),
			)
		} else {
			reqLog.Debug("query rejected due to invalid input",
				zap.Error(err),
				zap.String("user_id", req.UserID),
			)
		}
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
func (h *QueryHandler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

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
