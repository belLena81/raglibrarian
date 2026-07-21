package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
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
				writeRateLimited(w, r, window)
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
				writeRateLimited(w, r, remainingWindow(now, entry.started, window))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func FixedWindowPrincipalRateLimit(limit int, window time.Duration, maxKeys int) func(http.Handler) http.Handler {
	if limit < 1 || window <= 0 || maxKeys < 1 {
		panic("middleware: invalid principal rate limit")
	}
	var (
		mu      sync.Mutex
		entries = make(map[string]fixedWindowEntry)
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok || principal.UserID == "" || principal.Role == "" {
				writeRateLimited(w, r, window)
				return
			}
			key := principal.UserID + ":" + principal.Role
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
				writeRateLimited(w, r, window)
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
				writeRateLimited(w, r, remainingWindow(now, entry.started, window))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func BoundedConcurrency(limit int) func(http.Handler) http.Handler {
	if limit < 1 {
		panic("middleware: invalid concurrency limit")
	}
	tokens := make(chan struct{}, limit)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case tokens <- struct{}{}:
				defer func() { <-tokens }()
				next.ServeHTTP(w, r)
			default:
				writeRateLimited(w, r, time.Minute)
			}
		})
	}
}

func remainingWindow(now, started time.Time, window time.Duration) time.Duration {
	remaining := window - now.Sub(started)
	if remaining <= 0 {
		return time.Second
	}
	return remaining
}

func retryAfterSeconds(delay time.Duration) string {
	seconds := int(delay / time.Second)
	if delay%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}

func writeRateLimited(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	type errorResponse struct {
		Code      string `json:"code"`
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Code:      "rate_limited",
		Error:     "request limit exceeded",
		RequestID: chimiddleware.GetReqID(r.Context()),
	})
}
