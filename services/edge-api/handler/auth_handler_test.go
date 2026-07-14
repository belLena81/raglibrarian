package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

// ── Fake auth use case ────────────────────────────────────────────────────────

type fakeAuthUseCase struct {
	registerToken        string
	registerRefreshToken string
	registerUser         domain.User
	registerErr          error
	loginToken           string
	loginRefreshToken    string
	loginErr             error
	refreshTokens        auth.SessionTokens
	refreshErr           error
	logoutSessionID      string
	registerRole         domain.Role
}

func (f *fakeAuthUseCase) Register(_ context.Context, _ string, _ string, role domain.Role) (auth.SessionTokens, domain.User, error) {
	f.registerRole = role
	if f.registerErr != nil {
		return auth.SessionTokens{}, domain.User{}, f.registerErr
	}
	return auth.SessionTokens{AccessToken: f.registerToken, RefreshToken: f.registerRefreshToken, Role: string(f.registerUser.Role())}, f.registerUser, nil
}

func (f *fakeAuthUseCase) Login(_ context.Context, _, _ string) (auth.SessionTokens, error) {
	return auth.SessionTokens{AccessToken: f.loginToken, RefreshToken: f.loginRefreshToken, Role: "reader"}, f.loginErr
}

func (f *fakeAuthUseCase) Refresh(_ context.Context, _ string) (auth.SessionTokens, error) {
	return f.refreshTokens, f.refreshErr
}

func (f *fakeAuthUseCase) Logout(_ context.Context, sessionID string) error {
	f.logoutSessionID = sessionID
	return nil
}

func newRegisteredUser(t *testing.T) domain.User {
	t.Helper()
	u, err := domain.NewUser("alice@example.com", "hash", domain.RoleReader)
	require.NoError(t, err)
	return u
}

func newAuthHandler(t *testing.T, uc *fakeAuthUseCase) *handler.AuthHandler {
	t.Helper()
	return handler.NewAuthHandler(uc, zaptest.NewLogger(t))
}

// ── POST /auth/register ───────────────────────────────────────────────────────

func TestAuthHandler_Register_Returns201_WithToken(t *testing.T) {
	uc := &fakeAuthUseCase{
		registerToken: "v2.local.token",
		registerUser:  newRegisteredUser(t),
	}
	h := newAuthHandler(t, uc)

	body, _ := json.Marshal(handler.RegisterRequest{
		Email:    "alice@example.com",
		Password: "password123",
	})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Register(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)

	var resp handler.AuthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "v2.local.token", resp.Token)
	assert.Equal(t, "reader", resp.Role)
}

func TestAuthHandler_Register_DuplicateEmail_Returns409(t *testing.T) {
	uc := &fakeAuthUseCase{registerErr: domain.ErrEmailTaken}
	h := newAuthHandler(t, uc)

	body, _ := json.Marshal(handler.RegisterRequest{Email: "dup@example.com", Password: "pw"})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Register(rr, req)

	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestAuthHandler_Register_InvalidEmail_Returns422(t *testing.T) {
	uc := &fakeAuthUseCase{registerErr: domain.ErrInvalidEmail}
	h := newAuthHandler(t, uc)

	body, _ := json.Marshal(handler.RegisterRequest{Email: "bad", Password: "pw"})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Register(rr, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

func TestAuthHandler_Register_InvalidJSON_Returns400(t *testing.T) {
	uc := &fakeAuthUseCase{}
	h := newAuthHandler(t, uc)

	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString("{bad json"))
	rr := httptest.NewRecorder()
	h.Register(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAuthHandler_Register_RejectsClientControlledRole(t *testing.T) {
	uc := &fakeAuthUseCase{registerToken: "tok"}
	uc.registerUser, _ = domain.NewUser("r@e.com", "h", domain.RoleReader)
	h := newAuthHandler(t, uc)
	body, _ := json.Marshal(map[string]string{"email": "r@e.com", "password": "pw", "role": "admin"})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Register(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Empty(t, uc.registerRole)
}

// ── POST /auth/login ──────────────────────────────────────────────────────────

func TestAuthHandler_Login_ValidCredentials_Returns200WithToken(t *testing.T) {
	uc := &fakeAuthUseCase{loginToken: "v2.public.token", loginRefreshToken: "rotating-refresh"}
	h := newAuthHandler(t, uc)

	body, _ := json.Marshal(handler.LoginRequest{Email: "a@b.com", Password: "pw"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp handler.AuthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "v2.public.token", resp.Token)
	cookies := rr.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, "refresh_token", cookies[0].Name)
	assert.True(t, cookies[0].HttpOnly)
	assert.True(t, cookies[0].Secure)
	assert.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)
}

func TestAuthHandler_Refresh_RotatesCookieWithoutExposingRefreshToken(t *testing.T) {
	uc := &fakeAuthUseCase{refreshTokens: auth.SessionTokens{AccessToken: "new-access", RefreshToken: "new-refresh", Role: "reader"}}
	h := newAuthHandler(t, uc)
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "old-refresh"})
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Set-Cookie"), "refresh_token=new-refresh")
	assert.NotContains(t, rr.Body.String(), "new-refresh")
}

func TestAuthHandler_Refresh_InvalidToken_ClearsCookie(t *testing.T) {
	uc := &fakeAuthUseCase{refreshErr: domain.ErrInvalidCredentials}
	h := newAuthHandler(t, uc)
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "reused"})
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Header().Get("Set-Cookie"), "Max-Age=0")
}

func TestAuthHandler_Login_InvalidCredentials_Returns401(t *testing.T) {
	uc := &fakeAuthUseCase{loginErr: domain.ErrInvalidCredentials}
	h := newAuthHandler(t, uc)

	body, _ := json.Marshal(handler.LoginRequest{Email: "a@b.com", Password: "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAuthHandler_Login_InvalidJSON_Returns400(t *testing.T) {
	uc := &fakeAuthUseCase{}
	h := newAuthHandler(t, uc)

	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString("{bad"))
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── Wiring guards ─────────────────────────────────────────────────────────────

func TestNewAuthHandler_NilUseCase_Panics(t *testing.T) {
	assert.Panics(t, func() {
		handler.NewAuthHandler(nil, zaptest.NewLogger(t))
	})
}

func TestNewAuthHandler_NilLogger_Panics(t *testing.T) {
	assert.Panics(t, func() {
		handler.NewAuthHandler(&fakeAuthUseCase{}, nil)
	})
}
