package handler

import (
	"net/http"
	"strings"

	querymiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

// QueryRequest is the bounded public request for POST /query.
type QueryRequest struct {
	Question string `json:"question"`
}

type queryDiagnostics interface {
	RetrievalUnavailable(*http.Request)
}

// QueryHandler exposes only the truthful placeholder query boundary.
type QueryHandler struct{ diagnostics queryDiagnostics }

// NewQueryHandler constructs the placeholder query boundary.
func NewQueryHandler(diagnostics queryDiagnostics) *QueryHandler {
	if dependencyMissing(diagnostics) {
		panic("handler: Logger must not be nil")
	}
	return &QueryHandler{diagnostics: diagnostics}
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
	h.diagnostics.RetrievalUnavailable(r)
	writeError(w, http.StatusNotImplemented, "retrieval is unavailable")
}
