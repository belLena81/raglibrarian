package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
)

// contextKey is an unexported type for context keys to avoid collisions.
type contextKey string

const principalKey contextKey = "auth_principal"

// Authenticator validates the Authorization: Bearer token and stores Claims in context.
// Rejects with 401 if the header is absent or the token is invalid.
type tokenVerifier interface {
	Validate(string) (auth.Claims, error)
}

// SessionValidator is the narrow Identity contract required after local token
// verification. Identity is authoritative for revocation.
type SessionValidator interface {
	ValidateSession(context.Context, string, string) (authflow.Principal, error)
}

type authDiagnostics interface {
	TokenRejected(*http.Request)
}

// Authenticator validates a bearer token and stores verified claims in context.
func Authenticator(verifier tokenVerifier, sessions SessionValidator, diagnostics authDiagnostics) func(http.Handler) http.Handler {
	if dependencyMissing(verifier) || dependencyMissing(sessions) || dependencyMissing(diagnostics) {
		panic("middleware: authentication dependencies are required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr, ok := bearerToken(r)
			if !ok {
				writeUnauthorized(w, r)
				return
			}

			claims, err := verifier.Validate(tokenStr)
			if err != nil {
				diagnostics.TokenRejected(r)
				writeUnauthorized(w, r)
				return
			}
			if claims.SessionID == "" {
				writeUnauthorized(w, r)
				return
			}
			principal, err := sessions.ValidateSession(r.Context(), claims.UserID, claims.SessionID)
			if err != nil {
				if errors.Is(err, authflow.ErrInvalidCredentials) {
					writeUnauthorized(w, r)
					return
				}
				writeUnavailable(w, r)
				return
			}
			if principal.UserID != claims.UserID || principal.SessionID != claims.SessionID || principal.Status != "active" {
				writeUnauthorized(w, r)
				return
			}

			ctx := context.WithValue(r.Context(), principalKey, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeUnavailable(w http.ResponseWriter, r *http.Request) {
	writeBoundaryError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "authentication service unavailable")
}

// RequireRole enforces a minimum role. Must be applied after Authenticator.
// Panics if claims are absent, indicating incorrect middleware ordering.
func RequireRole(required auth.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				panic("middleware: RequireRole called without Authenticator in chain")
			}

			if required == auth.RoleAdmin && !principal.IsActiveAdmin() {
				writeForbidden(w, r)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireAnyRole admits an authenticated principal with one of the supplied roles.
func RequireAnyRole(roles ...auth.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				panic("middleware: RequireAnyRole called without Authenticator in chain")
			}
			for _, role := range roles {
				if auth.Role(principal.Role) == role {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeForbidden(w, r)
		})
	}
}

// ClaimsFromContext retrieves the verified Claims stored by Authenticator.
// Returns (Claims{}, false) if the route is public.
func ClaimsFromContext(ctx context.Context) (auth.Claims, bool) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return auth.Claims{}, false
	}
	return auth.Claims{UserID: principal.UserID, SessionID: principal.SessionID, Email: principal.Email, Role: auth.Role(principal.Role)}, true
}

// PrincipalFromContext returns the live Identity principal stored by Authenticator.
func PrincipalFromContext(ctx context.Context) (authflow.Principal, bool) {
	principal, ok := ctx.Value(principalKey).(authflow.Principal)
	return principal, ok
}

// WithClaims attaches already-validated claims. It is useful for trusted
// internal adapters and focused handler tests; public requests must use
// Authenticator.
func WithClaims(ctx context.Context, claims auth.Claims) context.Context {
	return WithPrincipal(ctx, authflow.Principal{UserID: claims.UserID, SessionID: claims.SessionID, Email: claims.Email, Role: string(claims.Role), Status: "active"})
}

// WithPrincipal attaches an already validated principal for trusted adapters and tests.
func WithPrincipal(ctx context.Context, principal authflow.Principal) context.Context {
	return context.WithValue(ctx, principalKey, principal)
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

func writeUnauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeBoundaryError(w, r, http.StatusUnauthorized, "unauthorized", "invalid or expired credentials")
}

func writeForbidden(w http.ResponseWriter, r *http.Request) {
	writeBoundaryError(w, r, http.StatusForbidden, "forbidden", "forbidden")
}

func writeBoundaryError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	type response struct {
		Code      string `json:"code"`
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, private")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response{Code: code, Error: message, RequestID: chimiddleware.GetReqID(r.Context())})
}
