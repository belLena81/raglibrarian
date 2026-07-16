package edgeapi_test

import (
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

func (fakeIdentity) Register(context.Context, string, string, string, string) error { return nil }
func (fakeIdentity) VerifyEmail(context.Context, string) error                      { return nil }
func (fakeIdentity) ResendVerification(context.Context, string) error               { return nil }
func (fakeIdentity) Login(context.Context, string, string) (authflow.Session, error) {
	return authflow.Session{}, nil
}
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
		handler.NewQueryHandler(diagnostics),
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
		edgeapi.NewRouter(handler.NewQueryHandler(diagnostics), handler.NewAuthHandler(identity, diagnostics, handler.CookieConfig{}), handler.NewHealthHandler(identity), handler.NewSetupHandler(identity), handler.NewAdminHandler(identity, handler.NewPendingHub(10)), verifier, nil, diagnostics, edgeapi.RouterConfig{})
	})
}

func testVerifier() (*auth.Verifier, error) {
	privateKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	return auth.NewVerifier(privateKey.Public().(ed25519.PublicKey))
}
