package handler_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"

	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

type fakeAuthUseCase struct{ registerErr, loginErr, refreshErr error }

func (f *fakeAuthUseCase) Register(context.Context, string, string) (authflow.Session, error) {
	return authflow.Session{AccessToken: "access", RefreshToken: "refresh", Role: "reader"}, f.registerErr
}
func (f *fakeAuthUseCase) Login(context.Context, string, string) (authflow.Session, error) {
	return authflow.Session{AccessToken: "access", RefreshToken: "refresh", Role: "reader"}, f.loginErr
}
func (f *fakeAuthUseCase) Refresh(context.Context, string) (authflow.Session, error) {
	return authflow.Session{AccessToken: "access", RefreshToken: "refresh", Role: "reader"}, f.refreshErr
}
func (*fakeAuthUseCase) Logout(context.Context, string) error { return nil }

func newHandler(t *testing.T, useCase *fakeAuthUseCase) *handler.AuthHandler {
	return handler.NewAuthHandler(useCase, zaptest.NewLogger(t), handler.CookieConfig{Secure: true})
}

func post(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body)))
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

func TestConstructorRequiresDependencies(t *testing.T) {
	assert.Panics(t, func() { handler.NewAuthHandler(nil, zaptest.NewLogger(t), handler.CookieConfig{}) })
	assert.Panics(t, func() { handler.NewAuthHandler(&fakeAuthUseCase{}, nil, handler.CookieConfig{}) })
}
