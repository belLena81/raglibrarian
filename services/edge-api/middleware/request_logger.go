// Package middleware contains HTTP middleware for the Edge service.
package middleware

import (
	"net/http"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
)

type requestDiagnostics interface {
	RequestCompleted(*http.Request, int, diagnostic.RequestOutcome, time.Duration, int)
}

// RequestLogger emits one allowlisted structured completion event per request.
func RequestLogger(diagnostics requestDiagnostics) func(http.Handler) http.Handler {
	if dependencyMissing(diagnostics) {
		panic("middleware: request diagnostics are required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			state := &completionState{}
			r = withCompletionState(r, state)
			defer func() {
				if recovered := recover(); recovered != nil {
					status := ww.Status()
					if state.hasStatus {
						status = state.status
					}
					outcome := diagnostic.RequestResponseAborted
					if state.hasOutcome {
						outcome = state.outcome
					} else if status == 0 {
						status = http.StatusInternalServerError
						outcome = diagnostic.RequestServerError
					}
					diagnostics.RequestCompleted(r, status, outcome, time.Since(start), ww.BytesWritten())
					panic(recovered)
				}
				status := ww.Status()
				if state.hasStatus {
					status = state.status
				}
				if status == 0 {
					status = http.StatusOK
				}
				outcome := requestOutcome(status)
				if state.hasOutcome {
					outcome = state.outcome
				}
				if isSuccessfulHealthProbe(r, status) {
					return
				}
				diagnostics.RequestCompleted(r, status, outcome, time.Since(start), ww.BytesWritten())
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

func isSuccessfulHealthProbe(r *http.Request, status int) bool {
	return r.Method == http.MethodGet && status < http.StatusBadRequest && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz")
}

func requestOutcome(status int) diagnostic.RequestOutcome {
	switch {
	case status == http.StatusNotImplemented:
		return diagnostic.RequestNotImplemented
	case status >= http.StatusInternalServerError:
		return diagnostic.RequestServerError
	case status >= http.StatusBadRequest:
		return diagnostic.RequestClientError
	default:
		return diagnostic.RequestSuccess
	}
}
