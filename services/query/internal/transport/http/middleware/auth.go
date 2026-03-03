// Package middleware provides chi-compatible auth middleware.
// It depends on tokenverifier.Verifier so the Query Service can swap in
// either the local PASETO verifier or the gRPC verifier without changing
// any middleware code.
package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/yourname/raglibrarian/pkg/tokenverifier"
)

// ── Context key ───────────────────────────────────────────────────────────────

type ctxKey struct{}

// ClaimsFromCtx retrieves verified claims from the request context.
// Returns nil when Authenticate was not applied or the token was rejected.
func ClaimsFromCtx(ctx context.Context) *tokenverifier.Claims {
	v, _ := ctx.Value(ctxKey{}).(*tokenverifier.Claims)
	return v
}

// ── Authenticate ──────────────────────────────────────────────────────────────

// Authenticate extracts the Bearer token, verifies it via v, and injects
// the resulting claims into the request context. Returns 401 on failure.
func Authenticate(v tokenverifier.Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				writeErr(w, http.StatusUnauthorized, "missing or malformed Authorization header")
				return
			}

			claims, err := v.Verify(r.Context(), raw)
			if err != nil {
				msg := "invalid token"
				if strings.Contains(err.Error(), "expired") {
					msg = "token has expired"
				}
				writeErr(w, http.StatusUnauthorized, msg)
				return
			}

			ctx := context.WithValue(r.Context(), ctxKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ── Role guards ───────────────────────────────────────────────────────────────

// RequireRole returns 403 when the authenticated user's role is not in the
// allowed list. Must be chained after Authenticate.
func RequireRole(allowed ...tokenverifier.Role) func(http.Handler) http.Handler {
	set := make(map[tokenverifier.Role]struct{}, len(allowed))
	for _, r := range allowed {
		set[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := ClaimsFromCtx(r.Context())
			if claims == nil {
				writeErr(w, http.StatusUnauthorized, "not authenticated")
				return
			}
			if _, ok := set[claims.Role]; !ok {
				writeErr(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Convenience guards —————————————————————————————————————————————————————————

// RequireReader allows any authenticated user (reader | librarian | admin).
func RequireReader(next http.Handler) http.Handler {
	return RequireRole(tokenverifier.RoleReader, tokenverifier.RoleLibrarian, tokenverifier.RoleAdmin)(next)
}

// RequireLibrarian allows librarian and admin.
func RequireLibrarian(next http.Handler) http.Handler {
	return RequireRole(tokenverifier.RoleLibrarian, tokenverifier.RoleAdmin)(next)
}

// RequireAdmin allows admin only.
func RequireAdmin(next http.Handler) http.Handler {
	return RequireRole(tokenverifier.RoleAdmin)(next)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	t := strings.TrimSpace(parts[1])
	return t, t != ""
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	msg = strings.ReplaceAll(msg, `"`, `'`)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
