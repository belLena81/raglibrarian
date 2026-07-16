package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

const requestIDBytes = 16

// RequestID replaces any client-supplied correlation value with a random,
// server-generated 128-bit identifier.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := make([]byte, requestIDBytes)
		if _, err := rand.Read(value); err != nil {
			w.Header().Set("Cache-Control", "no-store, private")
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		requestID := hex.EncodeToString(value)
		ctx := context.WithValue(r.Context(), chimiddleware.RequestIDKey, requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
