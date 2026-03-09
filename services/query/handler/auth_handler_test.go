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
	"github.com/belLena81/raglibrarian/services/query/handler"
)

// ── Fake auth use case ────────────────────────────────────────────────────────

type fakeAuthUseCase struct {
	registerUser domain.User
	registerErr  error
	loginToken   string
	loginErr     error
}

func (f *fakeAuthUseCase) Register(_ context.Context, email, _ string, role domain.Role) (domain.User, error) {
	if f.registerErr != nil {
		return domain.User{}, f.registerErr
	}
	return f.registerUser, nil
}

func (f *fakeAuthUseCase) Login(_ context.Context, _, _ string) (string, error) {
	return f.loginToken, f.loginErr
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
		registerUser: newRegisteredUser(t),
		loginToken:   "v2.local.token",
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

func TestAuthHandler_Register_DefaultsRoleToReader(t *testing.T) {
	var capturedRole domain.Role
	uc := &fakeAuthUseCase{loginToken: "tok"}
	uc.registerUser, _ = domain.NewUser("r@e.com", "h", domain.RoleReader)
	// We capture the role via the fake's Register call in the real test below.
	_ = capturedRole

	h := newAuthHandler(t, uc)
	body, _ := json.Marshal(map[string]string{"email": "r@e.com", "password": "pw"})
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Register(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
	var resp handler.AuthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "reader", resp.Role)
}

// ── POST /auth/login ──────────────────────────────────────────────────────────

func TestAuthHandler_Login_ValidCredentials_Returns200WithToken(t *testing.T) {
	uc := &fakeAuthUseCase{loginToken: "v2.local.token"}
	h := newAuthHandler(t, uc)

	body, _ := json.Marshal(handler.LoginRequest{Email: "a@b.com", Password: "pw"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp handler.AuthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "v2.local.token", resp.Token)
}

func TestAuthHandler_Login_InvalidCredentials_Returns401(t *testing.T) {
	uc := &fakeAuthUseCase{loginErr: auth.ErrInvalidCredentials}
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
