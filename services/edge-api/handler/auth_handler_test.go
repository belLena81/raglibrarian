package handler_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"

	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	qmiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

type fakeAuthUseCase struct {
	registerErr, loginErr, refreshErr error
	refreshCalls                      int
}

func (f *fakeAuthUseCase) Register(context.Context, string, string) (authflow.Session, error) {
	return authflow.Session{AccessToken: "access", RefreshToken: "refresh", Role: "reader"}, f.registerErr
}
func (f *fakeAuthUseCase) Login(context.Context, string, string) (authflow.Session, error) {
	return authflow.Session{AccessToken: "access", RefreshToken: "refresh", Role: "reader"}, f.loginErr
}
func (f *fakeAuthUseCase) Refresh(context.Context, string) (authflow.Session, error) {
	f.refreshCalls++
	return authflow.Session{AccessToken: "access", RefreshToken: "refresh", Role: "reader"}, f.refreshErr
}
func (*fakeAuthUseCase) Logout(context.Context, string) error { return nil }

func newHandler(t *testing.T, useCase *fakeAuthUseCase) *handler.AuthHandler {
	return handler.NewAuthHandler(useCase, diagnostic.New(zaptest.NewLogger(t)), handler.CookieConfig{Secure: true})
}

func post(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	qmiddleware.RequestID(h).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body)))
	return recorder
}

func TestRegisterMapsStableApplicationErrors(t *testing.T) {
	assert.Equal(t, http.StatusCreated, post(t, newHandler(t, &fakeAuthUseCase{}).Register, `{"email":"reader@example.com","password":"password-1234"}`).Code)
	assert.Equal(t, http.StatusConflict, post(t, newHandler(t, &fakeAuthUseCase{registerErr: authflow.ErrEmailTaken}).Register, `{"email":"reader@example.com","password":"password-1234"}`).Code)
	invalid := post(t, newHandler(t, &fakeAuthUseCase{registerErr: authflow.ErrInvalidRegistration}).Register, `{"email":"bad","password":"short"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, invalid.Code)
	assert.JSONEq(t, `{"error":"email or password is invalid"}`, invalid.Body.String())
	assert.Equal(t, http.StatusServiceUnavailable, post(t, newHandler(t, &fakeAuthUseCase{registerErr: authflow.ErrUnavailable}).Register, `{"email":"reader@example.com","password":"password-1234"}`).Code)
}

func TestLoginDistinguishesInvalidCredentialsFromOutage(t *testing.T) {
	body := `{"email":"reader@example.com","password":"password-1234"}`
	assert.Equal(t, http.StatusUnauthorized, post(t, newHandler(t, &fakeAuthUseCase{loginErr: authflow.ErrInvalidCredentials}).Login, body).Code)
	assert.Equal(t, http.StatusServiceUnavailable, post(t, newHandler(t, &fakeAuthUseCase{loginErr: authflow.ErrUnavailable}).Login, body).Code)
}

func TestRefreshUnavailablePreservesCookie(t *testing.T) {
	h := newHandler(t, &fakeAuthUseCase{refreshErr: authflow.ErrUnavailable})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	request.AddCookie(&http.Cookie{Name: "refresh_token", Value: "existing-refresh"})

	h.Refresh(recorder, request)

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Empty(t, recorder.Header().Values("Set-Cookie"))
	assert.JSONEq(t, `{"error":"authentication service unavailable"}`, recorder.Body.String())
}

func TestRefreshMissingCookieDoesNotCallIdentity(t *testing.T) {
	useCase := &fakeAuthUseCase{}
	h := newHandler(t, useCase)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)

	h.Refresh(recorder, request)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Zero(t, useCase.refreshCalls)
	cookies := recorder.Result().Cookies()
	if assert.Len(t, cookies, 1) {
		assert.Equal(t, "refresh_token", cookies[0].Name)
		assert.Less(t, cookies[0].MaxAge, 0)
	}
}

func TestRefreshUnexpectedFailurePreservesCookie(t *testing.T) {
	h := newHandler(t, &fakeAuthUseCase{refreshErr: errors.New("transport failure")})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	request.AddCookie(&http.Cookie{Name: "refresh_token", Value: "existing-refresh"})

	h.Refresh(recorder, request)

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Empty(t, recorder.Header().Values("Set-Cookie"))
}

func TestRefreshInvalidCredentialsClearsCookie(t *testing.T) {
	h := newHandler(t, &fakeAuthUseCase{refreshErr: authflow.ErrInvalidCredentials})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	request.AddCookie(&http.Cookie{Name: "refresh_token", Value: "invalid-refresh"})

	h.Refresh(recorder, request)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	cookies := recorder.Result().Cookies()
	if assert.Len(t, cookies, 1) {
		assert.Equal(t, "refresh_token", cookies[0].Name)
		assert.Empty(t, cookies[0].Value)
		assert.Less(t, cookies[0].MaxAge, 0)
		assert.True(t, cookies[0].HttpOnly)
		assert.True(t, cookies[0].Secure)
		assert.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)
	}
}

func TestRefreshSuccessReplacesCookieWithoutExposingIt(t *testing.T) {
	h := newHandler(t, &fakeAuthUseCase{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	request.AddCookie(&http.Cookie{Name: "refresh_token", Value: "existing-refresh"})

	h.Refresh(recorder, request)

	assert.Equal(t, http.StatusOK, recorder.Code)
	cookies := recorder.Result().Cookies()
	if assert.Len(t, cookies, 1) {
		assert.Equal(t, "refresh", cookies[0].Value)
	}
	assert.NotContains(t, recorder.Body.String(), "refresh")
}

func TestConstructorRequiresDependencies(t *testing.T) {
	assert.Panics(t, func() { handler.NewAuthHandler(nil, diagnostic.New(zaptest.NewLogger(t)), handler.CookieConfig{}) })
	assert.Panics(t, func() { handler.NewAuthHandler(&fakeAuthUseCase{}, nil, handler.CookieConfig{}) })
	var typedNil *diagnostic.Recorder
	assert.Panics(t, func() { handler.NewAuthHandler(&fakeAuthUseCase{}, typedNil, handler.CookieConfig{}) })
}

func TestRegisterDoesNotLogDependencyError(t *testing.T) {
	const canary = "sensitive-registration-error-canary"
	core, logs := observer.New(zapcore.DebugLevel)
	log := zap.New(core)
	h := handler.NewAuthHandler(
		&fakeAuthUseCase{registerErr: errors.New(canary)},
		diagnostic.New(log),
		handler.CookieConfig{Secure: true},
	)

	recorder := post(t, h.Register, `{"email":"reader@example.com","password":"password-1234"}`)

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	assert.Equal(t, "auth.register.failed", entry.Message)
	assert.NotContains(t, entry.Message, canary)
	for _, field := range entry.Context {
		assert.NotContains(t, field.String, canary)
	}
}
