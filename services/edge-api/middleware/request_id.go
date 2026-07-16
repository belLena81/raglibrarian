package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

const requestIDBytes = 16

type requestIDDiagnostics interface {
	RequestIDGenerationFailed()
}

// RequestID replaces any client-supplied correlation value with a random,
// server-generated 128-bit identifier.
func RequestID(diagnostics requestIDDiagnostics) func(http.Handler) http.Handler {
	return requestID(rand.Reader, diagnostics)
}

func requestID(random io.Reader, diagnostics requestIDDiagnostics) func(http.Handler) http.Handler {
	if random == nil {
		panic("middleware: request ID entropy source is required")
	}
	if dependencyMissing(diagnostics) {
		panic("middleware: request ID diagnostics are required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			value := make([]byte, requestIDBytes)
			if _, err := io.ReadFull(random, value); err != nil {
				applySecurityHeaders(w, r)
				w.Header().Set("Cache-Control", "no-store, private")
				diagnostics.RequestIDGenerationFailed()
				http.Error(w, "service unavailable", http.StatusServiceUnavailable)
				return
			}

			requestID := hex.EncodeToString(value)
			ctx := context.WithValue(r.Context(), chimiddleware.RequestIDKey, requestID)
			w.Header().Set("X-Request-ID", requestID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
