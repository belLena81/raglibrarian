package middleware

import (
	"encoding/json"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
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
				if recover() == nil {
					return
				}

				requestID := chimiddleware.GetReqID(r.Context())
				diagnostics.PanicRecovered(r)
				if ww.Status() != 0 {
					return
				}
				ww.Header().Set("Content-Type", "application/json")
				ww.Header().Set("Cache-Control", "no-store, private")
				ww.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(ww).Encode(recoveredErrorResponse{
					Code:      "internal_error",
					Error:     "internal server error",
					RequestID: requestID,
				})
			}()

			next.ServeHTTP(ww, r)
		})
	}
}
