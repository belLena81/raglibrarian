package middleware

import (
	"encoding/json"
	"errors"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
)

type recoveredErrorResponse struct {
	Code      string `json:"code"`
	Error     string `json:"error"`
	RequestID string `json:"request_id"`
}

type panicDiagnostics interface {
	PanicRecovered(*http.Request)
}

// Recovery converts panics into a stable error without exposing the panic
// value or stack.
func Recovery(diagnostics panicDiagnostics) func(http.Handler) http.Handler {
	if dependencyMissing(diagnostics) {
		panic("middleware: recovery diagnostics are required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				recoveredError, isError := recovered.(error)
				if isError && errors.Is(recoveredError, http.ErrAbortHandler) {
					setCompletionOutcome(r, diagnostic.RequestResponseAborted)
					panic(http.ErrAbortHandler)
				}

				requestID := chimiddleware.GetReqID(r.Context())
				diagnostics.PanicRecovered(r)
				if ww.Status() != 0 {
					setCompletionOutcome(r, diagnostic.RequestResponseAborted)
					panic(http.ErrAbortHandler)
				}
				ww.Header().Set("Content-Type", "application/json")
				ww.Header().Set("Cache-Control", "no-store, private")
				ww.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(ww).Encode(recoveredErrorResponse{
					Code:      "internal_error",
					Error:     "internal server error",
					RequestID: requestID,
				})
				setCompletionOutcome(r, diagnostic.RequestServerError)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}
