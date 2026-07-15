package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	qmiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

type fakeSessionValidator struct{ err error }

func (f fakeSessionValidator) ValidateSession(context.Context, string, string) error { return f.err }

func token(t *testing.T, issuer *auth.Issuer, sessionID string) string {
	t.Helper()
	value, err := issuer.Issue(auth.Subject{UserID: "user-1", Email: "reader@example.com", Role: auth.RoleReader, SessionID: sessionID})
	require.NoError(t, err)
	return value
}

type fakeVerifier struct {
	claims auth.Claims
}

func (f fakeVerifier) Validate(string) (auth.Claims, error) {
	return f.claims, nil
}

func request(t *testing.T, validator fakeSessionValidator, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	middleware := qmiddleware.Authenticator(issuer, validator, zaptest.NewLogger(t))
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := qmiddleware.ClaimsFromContext(r.Context())
		assert.True(t, ok)
		assert.Equal(t, "user-1", claims.UserID)
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token(t, issuer, sessionID))
	recorder := httptest.NewRecorder()
	middleware(next).ServeHTTP(recorder, req)
	return recorder
}

func TestAuthenticatorRequiresLiveSession(t *testing.T) {
	assert.Equal(t, http.StatusNoContent, request(t, fakeSessionValidator{}, "session-1").Code)
	assert.Equal(t, http.StatusUnauthorized, request(t, fakeSessionValidator{err: authflow.ErrInvalidCredentials}, "session-1").Code)
	assert.Equal(t, http.StatusServiceUnavailable, request(t, fakeSessionValidator{err: errors.New("transport")}, "session-1").Code)
	middleware := qmiddleware.Authenticator(fakeVerifier{claims: auth.Claims{UserID: "user-1"}}, fakeSessionValidator{}, zaptest.NewLogger(t))
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer legacy-token")
	middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestAuthenticatorRejectsMissingHeader(t *testing.T) {
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	qmiddleware.Authenticator(issuer, fakeSessionValidator{}, zaptest.NewLogger(t))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestRequireRoleRejectsReaderForAdmin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(qmiddleware.WithClaims(req.Context(), auth.Claims{Role: auth.RoleReader}))
	recorder := httptest.NewRecorder()
	qmiddleware.RequireRole(auth.RoleAdmin)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
}
