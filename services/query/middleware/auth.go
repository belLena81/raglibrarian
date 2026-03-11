package middleware

import (
	"context"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
)

// contextKey is an unexported type for context keys to avoid collisions.
type contextKey string

const claimsKey contextKey = "auth_claims"

// Authenticator validates the Authorization: Bearer token and stores Claims in context.
// Rejects with 401 if the header is absent or the token is invalid.
func Authenticator(issuer *auth.Issuer, log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr, ok := bearerToken(r)
			if !ok {
				writeUnauthorized(w, "missing or malformed Authorization header")
				return
			}

			claims, err := issuer.Validate(tokenStr)
			if err != nil {
				log.Debug("token validation failed", zap.Error(err))
				writeUnauthorized(w, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole enforces a minimum role. Must be applied after Authenticator.
// Panics if claims are absent, indicating incorrect middleware ordering.
func RequireRole(required domain.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok {
				panic("middleware: RequireRole called without Authenticator in chain")
			}

			if claims.Role != required && claims.Role != domain.RoleAdmin {
				writeForbidden(w, string(required)+" role required")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireMinRole enforces the privilege tier ordering: reader < librarian < admin.
// Any authenticated user whose role rank is >= the minimum rank is allowed through.
// Must be applied after Authenticator — panics on missing claims context.
//
// Use this instead of RequireRole whenever a route should be accessible to
// multiple roles (e.g. both librarian and admin can manage books).
func RequireMinRole(min domain.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok {
				panic("middleware: RequireMinRole called without Authenticator in chain")
			}

			if !claims.Role.AtLeast(min) {
				writeForbidden(w, "insufficient role: "+string(min)+" or above required")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ClaimsFromContext retrieves the verified Claims stored by Authenticator.
// Returns (Claims{}, false) if the route is public.
func ClaimsFromContext(ctx context.Context) (auth.Claims, bool) {
	claims, ok := ctx.Value(claimsKey).(auth.Claims)
	return claims, ok
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", false
	}
	return token, true
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}

func writeForbidden(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
