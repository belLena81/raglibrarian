package edgeapi_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/belLena81/raglibrarian/pkg/auth"
	edgeapi "github.com/belLena81/raglibrarian/services/edge-api"
	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

type fakeIdentity struct{}

type fakeRetrieval struct{}

func (fakeRetrieval) Search(context.Context, handler.SearchRequest) (handler.SearchResult, error) {
	return handler.SearchResult{Results: []handler.Evidence{}}, nil
}

func (fakeIdentity) Register(context.Context, string, string, string, string) error { return nil }
func (fakeIdentity) VerifyEmail(context.Context, string) error                      { return nil }
func (fakeIdentity) ResendVerification(context.Context, string) error               { return nil }
func (fakeIdentity) Login(context.Context, string, string, string) (authflow.Session, error) {
	return authflow.Session{}, nil
}
func (fakeIdentity) RequestPasswordReset(context.Context, string) error { return nil }
func (fakeIdentity) VerifyPasswordReset(context.Context, string, string) (string, []string, error) {
	return "", nil, nil
}
func (fakeIdentity) CompletePasswordReset(context.Context, string, string, string) error { return nil }
func (fakeIdentity) Refresh(context.Context, string) (authflow.Session, error) {
	return authflow.Session{}, authflow.ErrInvalidCredentials
}
func (fakeIdentity) Logout(context.Context, string) error { return nil }
func (fakeIdentity) ValidateSession(_ context.Context, userID, sessionID string) (authflow.Principal, error) {
	return authflow.Principal{UserID: userID, SessionID: sessionID, Role: "reader", Status: "active"}, nil
}
func (fakeIdentity) CheckReady(context.Context) error                                     { return nil }
func (fakeIdentity) SetupStatus(context.Context) (bool, error)                            { return false, nil }
func (fakeIdentity) BootstrapAdmin(context.Context, string, string, string, string) error { return nil }
func (fakeIdentity) ListPending(context.Context, authflow.Principal, int, string) (authflow.PendingPage, error) {
	return authflow.PendingPage{}, nil
}
func (fakeIdentity) Approve(context.Context, authflow.Principal, string) error { return nil }
func (fakeIdentity) Reject(context.Context, authflow.Principal, string) error  { return nil }

func TestRouterRequiresSessionValidatorAndAppliesSecurityHeaders(t *testing.T) {
	verifier, err := testVerifier()
	require.NoError(t, err)
	log := zaptest.NewLogger(t)
	diagnostics := diagnostic.New(log)
	identity := fakeIdentity{}
	hub := handler.NewPendingHub(10)
	router := edgeapi.NewRouter(
		handler.NewQueryHandler(fakeRetrieval{}),
		handler.NewAuthHandler(identity, diagnostics, handler.CookieConfig{Secure: true}),
		handler.NewHealthHandler(identity),
		handler.NewSetupHandler(identity),
		handler.NewAdminHandler(identity, hub),
		verifier,
		identity,
		diagnostics,
		edgeapi.RouterConfig{},
	)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	request.Header.Set("X-Request-ID", "client-controlled-request-id")
	router.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Equal(t, "no-store, private", recorder.Header().Get("Cache-Control"))
	assert.NotEqual(t, "client-controlled-request-id", recorder.Header().Get("X-Request-ID"))
	assert.Len(t, recorder.Header().Get("X-Request-ID"), 32)
}

func TestRouterPanicsWithoutSessionValidator(t *testing.T) {
	verifier, err := testVerifier()
	require.NoError(t, err)
	log := zaptest.NewLogger(t)
	diagnostics := diagnostic.New(log)
	identity := fakeIdentity{}
	assert.Panics(t, func() {
		edgeapi.NewRouter(handler.NewQueryHandler(fakeRetrieval{}), handler.NewAuthHandler(identity, diagnostics, handler.CookieConfig{}), handler.NewHealthHandler(identity), handler.NewSetupHandler(identity), handler.NewAdminHandler(identity, handler.NewPendingHub(10)), verifier, nil, diagnostics, edgeapi.RouterConfig{})
	})
}

func TestPasswordResetStagesHaveIndependentRateLimits(t *testing.T) {
	identity := &passwordResetIdentity{verifyErrors: []error{
		authflow.ErrInvalidPasswordReset,
		authflow.ErrInvalidPasswordReset,
		authflow.ErrInvalidPasswordReset,
		nil,
	}}
	router := newTestRouter(t, identity)

	assertPostStatus(t, router, "/auth/password-reset/request", `{"email":"reader@example.test"}`, http.StatusAccepted)
	for range 3 {
		assertPostStatus(t, router, "/auth/password-reset/verify", `{"email":"reader@example.test","code":"wrong"}`, http.StatusBadRequest)
	}
	assertPostStatus(t, router, "/auth/password-reset/verify", `{"email":"reader@example.test","code":"123456"}`, http.StatusOK)
	assertPostStatus(t, router, "/auth/password-reset/complete", `{"reset_grant":"grant","role":"reader","password":"password-1234"}`, http.StatusNoContent)

	stages := []struct {
		name       string
		path       string
		body       string
		wantStatus int
	}{
		{name: "request", path: "/auth/password-reset/request", body: `{"email":"reader@example.test"}`, wantStatus: http.StatusAccepted},
		{name: "verify", path: "/auth/password-reset/verify", body: `{"email":"reader@example.test","code":"123456"}`, wantStatus: http.StatusOK},
		{name: "complete", path: "/auth/password-reset/complete", body: `{"reset_grant":"grant","role":"reader","password":"password-1234"}`, wantStatus: http.StatusNoContent},
	}
	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			router := newTestRouter(t, &passwordResetIdentity{})
			for range 5 {
				assertPostStatus(t, router, stage.path, stage.body, stage.wantStatus)
			}
			assertPostStatus(t, router, stage.path, stage.body, http.StatusTooManyRequests)
		})
	}
}

type passwordResetIdentity struct {
	fakeIdentity
	verifyErrors []error
	verifyCalls  int
}

func (i *passwordResetIdentity) VerifyPasswordReset(context.Context, string, string) (string, []string, error) {
	if i.verifyCalls >= len(i.verifyErrors) {
		return "grant", []string{"reader"}, nil
	}
	err := i.verifyErrors[i.verifyCalls]
	i.verifyCalls++
	return "grant", []string{"reader"}, err
}

func newTestRouter(t *testing.T, identity *passwordResetIdentity) http.Handler {
	t.Helper()
	verifier, err := testVerifier()
	require.NoError(t, err)
	diagnostics := diagnostic.New(zaptest.NewLogger(t))
	return edgeapi.NewRouter(
		handler.NewQueryHandler(fakeRetrieval{}),
		handler.NewAuthHandler(identity, diagnostics, handler.CookieConfig{Secure: true}),
		handler.NewHealthHandler(identity),
		handler.NewSetupHandler(identity),
		handler.NewAdminHandler(identity, handler.NewPendingHub(10)),
		verifier,
		identity,
		diagnostics,
		edgeapi.RouterConfig{},
	)
}

func assertPostStatus(t *testing.T, router http.Handler, path, body string, want int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	assert.Equal(t, want, recorder.Code, "%s response: %s", path, recorder.Body.String())
}

func testVerifier() (*auth.Verifier, error) {
	privateKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	return auth.NewVerifier(privateKey.Public().(ed25519.PublicKey))
}
