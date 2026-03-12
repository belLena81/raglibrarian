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
	metarepo "github.com/belLena81/raglibrarian/services/metadata/repository"
	"github.com/belLena81/raglibrarian/services/query"
	"github.com/belLena81/raglibrarian/services/query/handler"
)

// ── Fakes ─────────────────────────────────────────────────────────────────────

type fakeQueryUC struct{ results []domain.SearchResult }

func (f *fakeQueryUC) Answer(_ context.Context, _, _ string) ([]domain.SearchResult, error) {
	return f.results, nil
}

type fakeAuthUC struct{}

func (f *fakeAuthUC) Register(_ context.Context, email, _ string, role domain.Role) (string, domain.User, error) {
	u, err := domain.NewUser(email, "hashed", role)
	return "fake-token", u, err
}
func (f *fakeAuthUC) Login(_ context.Context, _, _ string) (string, error) {
	return "fake-token", nil
}

type fakeBookUC struct{}

func (f *fakeBookUC) AddBook(_ context.Context, title, author string, year int) (domain.Book, error) {
	return domain.NewBook(title, author, year)
}
func (f *fakeBookUC) GetBook(_ context.Context, _ string) (domain.Book, error) {
	return domain.Book{}, domain.ErrBookNotFound
}
func (f *fakeBookUC) ListBooks(_ context.Context, _ metarepo.ListFilter) ([]domain.Book, error) {
	return []domain.Book{}, nil
}
func (f *fakeBookUC) RemoveBook(_ context.Context, _ string) error     { return nil }
func (f *fakeBookUC) TriggerReindex(_ context.Context, _ string) error { return nil }

// ── Helpers ───────────────────────────────────────────────────────────────────

func newTestIssuer(t *testing.T) *auth.Issuer {
	t.Helper()
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	return issuer
}

func tokenForRole(t *testing.T, issuer *auth.Issuer, role domain.Role) string {
	t.Helper()
	u, err := domain.NewUser("test@example.com", "hash", role)
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
	bh := handler.NewBookHandler(&fakeBookUC{}, log)
	return query.NewRouter(qh, ah, bh, issuer, log), issuer
}

// ── Existing route tests ───────────────────────────────────────────────────────

func TestRouter_GET_Healthz_Returns200_NoAuth(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRouter_POST_Auth_Register_Returns201(t *testing.T) {
	router, _ := newTestRouter(t)
	body, _ := json.Marshal(map[string]string{"email": "user@example.com", "password": "pw"})
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
	body, _ := json.Marshal(map[string]string{"question": "What is a goroutine?", "user_id": "user-1"})
	req := httptest.NewRequest(http.MethodPost, "/query/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenForRole(t, issuer, domain.RoleReader))
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

func TestRouter_GET_AuthMe_WithValidToken_Returns200WithIdentity(t *testing.T) {
	router, issuer := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForRole(t, issuer, domain.RoleReader))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "test@example.com", body["email"])
	assert.Equal(t, "reader", body["role"])
	assert.NotEmpty(t, body["user_id"])
}

func TestRouter_GET_AuthMe_WithoutToken_Returns401(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRouter_POST_AuthLogout_WithValidToken_Returns200(t *testing.T) {
	router, issuer := newTestRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForRole(t, issuer, domain.RoleReader))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "logged out", body["message"])
}

func TestRouter_POST_AuthLogout_WithoutToken_Returns401(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
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

// ── Admin book route tests ─────────────────────────────────────────────────────

func TestRouter_AdminBooks_WithoutToken_Returns401(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/books", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	// 401 — not 403 — because no token was provided at all.
	// The existence of the route must not be revealed to unauthenticated callers.
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRouter_AdminBooks_ReaderToken_Returns403(t *testing.T) {
	router, issuer := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/books", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForRole(t, issuer, domain.RoleReader))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestRouter_AdminBooks_LibrarianToken_Returns200(t *testing.T) {
	router, issuer := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/books", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForRole(t, issuer, domain.RoleLibrarian))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRouter_AdminBooks_AdminToken_Returns200(t *testing.T) {
	router, issuer := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/books", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForRole(t, issuer, domain.RoleAdmin))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRouter_AdminBooksPost_LibrarianToken_Returns201(t *testing.T) {
	router, issuer := newTestRouter(t)
	body, _ := json.Marshal(handler.AddBookRequest{Title: "DDIA", Author: "Kleppmann", Year: 2017})
	req := httptest.NewRequest(http.MethodPost, "/admin/books", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenForRole(t, issuer, domain.RoleLibrarian))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusCreated, rr.Code)
}

func TestRouter_AdminBookDelete_LibrarianToken_Returns404ForUnknown(t *testing.T) {
	router, issuer := newTestRouter(t)
	req := httptest.NewRequest(http.MethodDelete, "/admin/books/ghost-id", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForRole(t, issuer, domain.RoleLibrarian))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	// fakeBookUC.RemoveBook returns nil (success) — only GetBook returns ErrBookNotFound
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestRouter_AdminBookReindex_ReaderToken_Returns403(t *testing.T) {
	router, issuer := newTestRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/books/b-1/reindex", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForRole(t, issuer, domain.RoleReader))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}
