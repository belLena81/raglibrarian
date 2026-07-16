package middleware

import (
	"net/http"
	"strings"
)

// SecurityHeaders applies browser hardening and private-response cache policy.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		applySecurityHeaders(w, r)
		next.ServeHTTP(w, r)
	})
}

func applySecurityHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	if strings.HasPrefix(r.URL.Path, "/auth/") || r.URL.Path == "/query" || strings.HasPrefix(r.URL.Path, "/query/") {
		w.Header().Set("Cache-Control", "no-store")
	}
}
