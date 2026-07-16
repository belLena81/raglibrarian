package handler

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	querymiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

// QueryRequest is the bounded public request for POST /query.
type QueryRequest struct {
	Question string `json:"question"`
}

// QueryHandler exposes only the truthful Milestone 1 query boundary.
type QueryHandler struct{ log *zap.Logger }

// NewQueryHandler constructs the Milestone 1 query boundary.
func NewQueryHandler(log *zap.Logger) *QueryHandler {
	if log == nil {
		panic("handler: Logger must not be nil")
	}
	return &QueryHandler{log: log}
}

// Query validates the request and reports that retrieval is not delivered yet.
func (h *QueryHandler) Query(w http.ResponseWriter, r *http.Request) {
	var req QueryRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	_, ok := querymiddleware.ClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid query")
		return
	}
	h.log.Debug("query.retrieval.unavailable",
		zap.String("request_id", middleware.GetReqID(r.Context())),
		zap.String("outcome", "not_implemented"),
	)
	writeError(w, http.StatusNotImplemented, "retrieval is unavailable in milestone 1")
}
