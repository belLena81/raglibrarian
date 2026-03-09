package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	qmiddleware "github.com/belLena81/raglibrarian/services/query/middleware"
)

func newIssuer(t *testing.T) *auth.Issuer {
	t.Helper()
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	return issuer
}

func validToken(t *testing.T, issuer *auth.Issuer, role domain.Role) string {
	t.Helper()
	u, err := domain.NewUser("test@example.com", "hash", role)
	require.NoError(t, err)
	token, err := issuer.Issue(u)
	require.NoError(t, err)
	return token
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// ── Authenticator ─────────────────────────────────────────────────────────────

func TestAuthenticator_ValidToken_Passes(t *testing.T) {
	issuer := newIssuer(t)
	mw := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleReader))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAuthenticator_MissingHeader_Returns401(t *testing.T) {
	issuer := newIssuer(t)
	mw := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAuthenticator_InvalidToken_Returns401(t *testing.T) {
	issuer := newIssuer(t)
	mw := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAuthenticator_BearerScheme_CaseInsensitive(t *testing.T) {
	issuer := newIssuer(t)
	mw := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))
	handler := mw(okHandler())

	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		t.Run(scheme, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", scheme+" "+validToken(t, issuer, domain.RoleReader))
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusOK, rr.Code)
		})
	}
}

func TestAuthenticator_WrongScheme_Returns401(t *testing.T) {
	issuer := newIssuer(t)
	mw := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAuthenticator_StoresClaimsInContext(t *testing.T) {
	issuer := newIssuer(t)
	mw := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))

	var capturedClaims auth.Claims
	captureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := qmiddleware.ClaimsFromContext(r.Context())
		assert.True(t, ok)
		capturedClaims = claims
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleAdmin))
	rr := httptest.NewRecorder()
	mw(captureHandler).ServeHTTP(rr, req)

	assert.Equal(t, "test@example.com", capturedClaims.Email)
	assert.Equal(t, domain.RoleAdmin, capturedClaims.Role)
}

func TestAuthenticator_Sets_WWWAuthenticate_Header(t *testing.T) {
	issuer := newIssuer(t)
	mw := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, "Bearer", rr.Header().Get("WWW-Authenticate"))
}

// ── RequireRole ───────────────────────────────────────────────────────────────

func TestRequireRole_Admin_AllowsAdmin(t *testing.T) {
	issuer := newIssuer(t)
	authMW := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))
	roleMW := qmiddleware.RequireRole(domain.RoleAdmin)
	handler := authMW(roleMW(okHandler()))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleAdmin))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRequireRole_Admin_BlocksReader(t *testing.T) {
	issuer := newIssuer(t)
	authMW := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))
	roleMW := qmiddleware.RequireRole(domain.RoleAdmin)
	handler := authMW(roleMW(okHandler()))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleReader))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestRequireRole_Reader_AllowsReader(t *testing.T) {
	issuer := newIssuer(t)
	authMW := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))
	roleMW := qmiddleware.RequireRole(domain.RoleReader)
	handler := authMW(roleMW(okHandler()))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleReader))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── ClaimsFromContext ─────────────────────────────────────────────────────────

func TestClaimsFromContext_NoClaims_ReturnsFalse(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, ok := qmiddleware.ClaimsFromContext(req.Context())
	assert.False(t, ok)
}
