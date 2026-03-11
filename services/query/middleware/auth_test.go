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
// RequireRole is the exact-match variant: the caller's role must equal the
// required role, or they must be admin (which always passes everything).

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

func TestRequireRole_Admin_BlocksLibrarian(t *testing.T) {
	issuer := newIssuer(t)
	authMW := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))
	roleMW := qmiddleware.RequireRole(domain.RoleAdmin)
	handler := authMW(roleMW(okHandler()))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleLibrarian))
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

func TestRequireRole_WithoutAuthenticator_Panics(t *testing.T) {
	// RequireRole must panic when it is placed in the chain without
	// Authenticator running first, so misconfigured routers are caught
	// immediately at development time — not silently in production.
	roleMW := qmiddleware.RequireRole(domain.RoleAdmin)
	handler := roleMW(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	assert.Panics(t, func() { handler.ServeHTTP(rr, req) })
}

// ── RequireMinRole ────────────────────────────────────────────────────────────
// RequireMinRole enforces reader < librarian < admin ordering.
// Any role that meets or exceeds the minimum is allowed through.

func TestRequireMinRole_MinAdmin_AllowsAdmin(t *testing.T) {
	issuer := newIssuer(t)
	handler := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))(
		qmiddleware.RequireMinRole(domain.RoleAdmin)(okHandler()),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleAdmin))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRequireMinRole_MinAdmin_BlocksLibrarian(t *testing.T) {
	issuer := newIssuer(t)
	handler := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))(
		qmiddleware.RequireMinRole(domain.RoleAdmin)(okHandler()),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleLibrarian))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestRequireMinRole_MinAdmin_BlocksReader(t *testing.T) {
	issuer := newIssuer(t)
	handler := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))(
		qmiddleware.RequireMinRole(domain.RoleAdmin)(okHandler()),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleReader))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestRequireMinRole_MinLibrarian_AllowsAdmin(t *testing.T) {
	issuer := newIssuer(t)
	handler := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))(
		qmiddleware.RequireMinRole(domain.RoleLibrarian)(okHandler()),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleAdmin))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRequireMinRole_MinLibrarian_AllowsLibrarian(t *testing.T) {
	issuer := newIssuer(t)
	handler := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))(
		qmiddleware.RequireMinRole(domain.RoleLibrarian)(okHandler()),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleLibrarian))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRequireMinRole_MinLibrarian_BlocksReader(t *testing.T) {
	issuer := newIssuer(t)
	handler := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))(
		qmiddleware.RequireMinRole(domain.RoleLibrarian)(okHandler()),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleReader))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestRequireMinRole_MinReader_AllowsAll(t *testing.T) {
	issuer := newIssuer(t)
	mw := qmiddleware.RequireMinRole(domain.RoleReader)

	for _, role := range []domain.Role{domain.RoleReader, domain.RoleLibrarian, domain.RoleAdmin} {
		t.Run(string(role), func(t *testing.T) {
			handler := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))(
				mw(okHandler()),
			)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, role))
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusOK, rr.Code)
		})
	}
}

func TestRequireMinRole_ForbiddenResponse_HasJSONContentType(t *testing.T) {
	// Clients that parse error bodies require a consistent Content-Type.
	issuer := newIssuer(t)
	handler := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))(
		qmiddleware.RequireMinRole(domain.RoleAdmin)(okHandler()),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleReader))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")
}

func TestRequireMinRole_ForbiddenResponse_BodyContainsErrorKey(t *testing.T) {
	issuer := newIssuer(t)
	handler := qmiddleware.Authenticator(issuer, zaptest.NewLogger(t))(
		qmiddleware.RequireMinRole(domain.RoleLibrarian)(okHandler()),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+validToken(t, issuer, domain.RoleReader))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Contains(t, rr.Body.String(), `"error"`)
}

func TestRequireMinRole_WithoutAuthenticator_Panics(t *testing.T) {
	// Same safety contract as RequireRole: panic fast on middleware misorder.
	handler := qmiddleware.RequireMinRole(domain.RoleReader)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	assert.Panics(t, func() { handler.ServeHTTP(rr, req) })
}

// ── ClaimsFromContext ─────────────────────────────────────────────────────────

func TestClaimsFromContext_NoClaims_ReturnsFalse(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, ok := qmiddleware.ClaimsFromContext(req.Context())
	assert.False(t, ok)
}
