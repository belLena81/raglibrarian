package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

type fixedWindowEntry struct {
	started time.Time
	count   int
}

// FixedWindowRateLimit bounds abuse without retaining raw addresses beyond
// the process lifetime. Production ingress applies the authoritative limit.
func FixedWindowRateLimit(limit int, window time.Duration, maxKeys int) func(http.Handler) http.Handler {
	if limit < 1 || window <= 0 || maxKeys < 1 {
		panic("middleware: invalid rate limit")
	}
	var (
		mu      sync.Mutex
		entries = make(map[string]fixedWindowEntry)
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				key = "unknown"
			}
			now := time.Now()
			mu.Lock()
			entry, exists := entries[key]
			if !exists && len(entries) >= maxKeys {
				for existingKey, existing := range entries {
					if now.Sub(existing.started) >= window {
						delete(entries, existingKey)
					}
				}
			}
			if !exists && len(entries) >= maxKeys {
				mu.Unlock()
				writeRateLimited(w, r)
				return
			}
			if !exists || now.Sub(entry.started) >= window {
				entry = fixedWindowEntry{started: now}
			}
			entry.count++
			entries[key] = entry
			allowed := entry.count <= limit
			mu.Unlock()
			if !allowed {
				writeRateLimited(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeRateLimited(w http.ResponseWriter, r *http.Request) {
	type errorResponse struct {
		Code      string `json:"code"`
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Retry-After", "60")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Code:      "rate_limited",
		Error:     "request limit exceeded",
		RequestID: chimiddleware.GetReqID(r.Context()),
	})
}
