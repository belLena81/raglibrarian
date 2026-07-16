package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
)

// contextKey is an unexported type for context keys to avoid collisions.
type contextKey string

const claimsKey contextKey = "auth_claims"

// Authenticator validates the Authorization: Bearer token and stores Claims in context.
// Rejects with 401 if the header is absent or the token is invalid.
type tokenVerifier interface {
	Validate(string) (auth.Claims, error)
}

// SessionValidator is the narrow Identity contract required after local token
// verification. Identity is authoritative for revocation.
type SessionValidator interface {
	ValidateSession(context.Context, string, string) error
}

// Authenticator validates a bearer token and stores verified claims in context.
func Authenticator(verifier tokenVerifier, sessions SessionValidator, log *zap.Logger) func(http.Handler) http.Handler {
	if verifier == nil || sessions == nil || log == nil {
		panic("middleware: authentication dependencies are required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr, ok := bearerToken(r)
			if !ok {
				writeUnauthorized(w, "missing or malformed Authorization header")
				return
			}

			claims, err := verifier.Validate(tokenStr)
			if err != nil {
				log.Debug("auth.token.rejected", zap.String("outcome", "invalid_token"))
				writeUnauthorized(w, "invalid or expired token")
				return
			}
			if claims.SessionID == "" {
				writeUnauthorized(w, "invalid or expired token")
				return
			}
			if err := sessions.ValidateSession(r.Context(), claims.UserID, claims.SessionID); err != nil {
				if errors.Is(err, authflow.ErrInvalidCredentials) {
					writeUnauthorized(w, "invalid or expired token")
					return
				}
				writeUnavailable(w)
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"authentication service unavailable"}`))
}

// RequireRole enforces a minimum role. Must be applied after Authenticator.
// Panics if claims are absent, indicating incorrect middleware ordering.
func RequireRole(required auth.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok {
				panic("middleware: RequireRole called without Authenticator in chain")
			}

			if required == auth.RoleAdmin && claims.Role != auth.RoleAdmin {
				writeForbidden(w, "admin role required")
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

// WithClaims attaches already-validated claims. It is useful for trusted
// internal adapters and focused handler tests; public requests must use
// Authenticator.
func WithClaims(ctx context.Context, claims auth.Claims) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
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
