package middleware

import (
	"context"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
)

// contextKey is an unexported type for context keys in this package.
// Using a named type prevents collisions with keys from other packages
// that also use plain strings.
type contextKey string

const claimsKey contextKey = "auth_claims"

// Authenticator returns a chi middleware that:
//  1. Reads the Authorization: Bearer <token> header
//  2. Validates the PASETO token using the provided Issuer
//  3. Stores the verified Claims in the request context
//  4. Rejects with 401 if the header is absent or the token is invalid
//
// Routes that require authentication must be registered under a router
// that has this middleware applied. Public routes (/healthz, /auth/*) must
// be outside that sub-router.
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
				// Log at Debug — failed auth is not operator-actionable noise.
				log.Debug("token validation failed", zap.Error(err))
				writeUnauthorized(w, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole returns a middleware that enforces a minimum role.
// Must be applied after Authenticator — panics if claims are absent,
// which indicates incorrect middleware ordering.
func RequireRole(required domain.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok {
				// Authenticator was not applied — this is a programmer error.
				panic("middleware: RequireRole called without Authenticator in chain")
			}

			if required == domain.RoleAdmin && !claims.Role.CanWrite() {
				writeForbidden(w, "admin role required")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ClaimsFromContext retrieves the verified Claims stored by Authenticator.
// Returns (Claims{}, false) if no claims are present — i.e. the route is public.
func ClaimsFromContext(ctx context.Context) (auth.Claims, bool) {
	claims, ok := ctx.Value(claimsKey).(auth.Claims)
	return claims, ok
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// bearerToken extracts the token string from an "Authorization: Bearer <token>"
// header. Returns ("", false) if the header is absent or malformed.
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
