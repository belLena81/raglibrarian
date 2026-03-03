package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/yourname/raglibrarian/pkg/tokenverifier"
	"github.com/yourname/raglibrarian/services/query/internal/transport/http/middleware"
)

// ── stub verifier ─────────────────────────────────────────────────────────────

type stubVerifier struct {
	claims *tokenverifier.Claims
	err    error
}

func (s *stubVerifier) Verify(_ context.Context, _ string) (*tokenverifier.Claims, error) {
	return s.claims, s.err
}

func validClaims(role tokenverifier.Role) *tokenverifier.Claims {
	return &tokenverifier.Claims{UserID: uuid.New(), Email: "ada@example.com", Role: role}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func requestWithBearer(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// ── Authenticate ──────────────────────────────────────────────────────────────

func TestAuthenticate_ValidToken_Passes(t *testing.T) {
	v := &stubVerifier{claims: validClaims(tokenverifier.RoleReader)}
	rr := httptest.NewRecorder()
	middleware.Authenticate(v)(okHandler()).ServeHTTP(rr, requestWithBearer("good-token"))
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAuthenticate_MissingHeader_401(t *testing.T) {
	v := &stubVerifier{claims: validClaims(tokenverifier.RoleReader)}
	rr := httptest.NewRecorder()
	middleware.Authenticate(v)(okHandler()).ServeHTTP(rr, requestWithBearer(""))
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAuthenticate_VerifierRejectsToken_401(t *testing.T) {
	v := &stubVerifier{err: errors.New("invalid token")}
	rr := httptest.NewRecorder()
	middleware.Authenticate(v)(okHandler()).ServeHTTP(rr, requestWithBearer("bad"))
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAuthenticate_InjectsClaimsIntoContext(t *testing.T) {
	want := validClaims(tokenverifier.RoleAdmin)
	v := &stubVerifier{claims: want}

	var got *tokenverifier.Claims
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = middleware.ClaimsFromCtx(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	middleware.Authenticate(v)(capture).ServeHTTP(httptest.NewRecorder(), requestWithBearer("tok"))
	assert.Equal(t, want.Role, got.Role)
	assert.Equal(t, want.Email, got.Email)
}

// ── RequireRole ───────────────────────────────────────────────────────────────

func authedRequest(role tokenverifier.Role) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := context.WithValue(r.Context(), struct{ middleware.CtxKeyExport }{}, &tokenverifier.Claims{
		UserID: uuid.New(), Email: "ada@example.com", Role: role,
	})
	// Inject via actual middleware path using stub
	v := &stubVerifier{claims: &tokenverifier.Claims{UserID: uuid.New(), Email: "x", Role: role}}
	r.Header.Set("Authorization", "Bearer tok")
	var enriched *http.Request
	middleware.Authenticate(v)(http.HandlerFunc(func(_ http.ResponseWriter, rr *http.Request) {
		enriched = rr
	})).ServeHTTP(httptest.NewRecorder(), r)
	_ = ctx
	return enriched
}

func TestRequireRole_AllowedRole_200(t *testing.T) {
	req := authedRequest(tokenverifier.RoleAdmin)
	rr := httptest.NewRecorder()
	middleware.RequireRole(tokenverifier.RoleAdmin)(okHandler()).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRequireRole_ForbiddenRole_403(t *testing.T) {
	req := authedRequest(tokenverifier.RoleReader)
	rr := httptest.NewRecorder()
	middleware.RequireRole(tokenverifier.RoleAdmin)(okHandler()).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestRequireReader_AllRolesPass(t *testing.T) {
	for _, role := range []tokenverifier.Role{tokenverifier.RoleReader, tokenverifier.RoleLibrarian, tokenverifier.RoleAdmin} {
		req := authedRequest(role)
		rr := httptest.NewRecorder()
		middleware.RequireReader(okHandler()).ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code, "role: %s", role)
	}
}

func TestRequireLibrarian_BlocksReader(t *testing.T) {
	req := authedRequest(tokenverifier.RoleReader)
	rr := httptest.NewRecorder()
	middleware.RequireLibrarian(okHandler()).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestRequireAdmin_BlocksLibrarian(t *testing.T) {
	req := authedRequest(tokenverifier.RoleLibrarian)
	rr := httptest.NewRecorder()
	middleware.RequireAdmin(okHandler()).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestRequireRole_NoClaims_401(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil) // no context injection
	rr := httptest.NewRecorder()
	middleware.RequireRole(tokenverifier.RoleReader)(okHandler()).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
