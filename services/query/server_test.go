package query_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/query"
	"github.com/belLena81/raglibrarian/services/query/handler"
)

// ── Fakes ─────────────────────────────────────────────────────────────────────

type fakeQueryUC struct{ results []domain.SearchResult }

func (f *fakeQueryUC) Answer(_ context.Context, _, _ string) ([]domain.SearchResult, error) {
	return f.results, nil
}

type fakeAuthUC struct{}

func (f *fakeAuthUC) Register(_ context.Context, email, password string, role domain.Role) (domain.User, error) {
	return domain.NewUser(email, "hashed", role)
}
func (f *fakeAuthUC) Login(_ context.Context, _, _ string) (string, error) {
	return "fake-token", nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func newTestIssuer(t *testing.T) *auth.Issuer {
	t.Helper()
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	return issuer
}

func validAuthToken(t *testing.T, issuer *auth.Issuer) string {
	t.Helper()
	u, err := domain.NewUser("test@example.com", "hash", domain.RoleReader)
	require.NoError(t, err)
	token, err := issuer.Issue(u)
	require.NoError(t, err)
	return token
}

func newTestRouter(t *testing.T) (http.Handler, *auth.Issuer) {
	t.Helper()
	log := zaptest.NewLogger(t)
	issuer := newTestIssuer(t)
	qh := handler.NewQueryHandler(&fakeQueryUC{}, log)
	ah := handler.NewAuthHandler(&fakeAuthUC{}, log)
	return query.NewRouter(qh, ah, issuer, log), issuer
}

// ── Route tests ───────────────────────────────────────────────────────────────

func TestRouter_GET_Healthz_Returns200_NoAuth(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRouter_POST_Auth_Register_Returns201(t *testing.T) {
	router, _ := newTestRouter(t)
	body, _ := json.Marshal(map[string]string{
		"email": "user@example.com", "password": "pw",
	})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusCreated, rr.Code)
}

func TestRouter_POST_Auth_Login_Returns200(t *testing.T) {
	router, _ := newTestRouter(t)
	body, _ := json.Marshal(map[string]string{"email": "u@e.com", "password": "pw"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRouter_POST_Query_WithValidToken_Returns200(t *testing.T) {
	router, issuer := newTestRouter(t)
	body, _ := json.Marshal(map[string]string{
		"question": "What is a goroutine?", "user_id": "user-1",
	})
	req := httptest.NewRequest(http.MethodPost, "/query/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+validAuthToken(t, issuer))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRouter_POST_Query_WithoutToken_Returns401(t *testing.T) {
	router, _ := newTestRouter(t)
	body, _ := json.Marshal(map[string]string{"question": "test?", "user_id": "u"})
	req := httptest.NewRequest(http.MethodPost, "/query/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRouter_POST_Query_WithInvalidToken_Returns401(t *testing.T) {
	router, _ := newTestRouter(t)
	body, _ := json.Marshal(map[string]string{"question": "test?", "user_id": "u"})
	req := httptest.NewRequest(http.MethodPost, "/query/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer invalid-token")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRouter_UnknownRoute_Returns404(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRouter_RequestID_IsInjected(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.NotEmpty(t, rr.Header().Get("X-Request-Id"))
}
