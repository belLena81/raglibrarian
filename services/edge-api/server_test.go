package edgeapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/belLena81/raglibrarian/pkg/auth"
	edgeapi "github.com/belLena81/raglibrarian/services/edge-api"
	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

type fakeIdentity struct{}

func (fakeIdentity) Register(context.Context, string, string) (authflow.Session, error) {
	return authflow.Session{Role: "reader"}, nil
}
func (fakeIdentity) Login(context.Context, string, string) (authflow.Session, error) {
	return authflow.Session{}, nil
}
func (fakeIdentity) Refresh(context.Context, string) (authflow.Session, error) {
	return authflow.Session{}, authflow.ErrInvalidCredentials
}
func (fakeIdentity) Logout(context.Context, string) error                  { return nil }
func (fakeIdentity) ValidateSession(context.Context, string, string) error { return nil }
func (fakeIdentity) CheckReady(context.Context) error                      { return nil }

func TestRouterRequiresSessionValidatorAndAppliesSecurityHeaders(t *testing.T) {
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	log := zaptest.NewLogger(t)
	identity := fakeIdentity{}
	router := edgeapi.NewRouter(
		handler.NewQueryHandler(log),
		handler.NewAuthHandler(identity, log, handler.CookieConfig{Secure: true}),
		handler.NewHealthHandler(identity),
		issuer,
		identity,
		log,
		edgeapi.RouterConfig{},
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/me", nil))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
}

func TestRouterPanicsWithoutSessionValidator(t *testing.T) {
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	log := zaptest.NewLogger(t)
	identity := fakeIdentity{}
	assert.Panics(t, func() {
		edgeapi.NewRouter(handler.NewQueryHandler(log), handler.NewAuthHandler(identity, log, handler.CookieConfig{}), handler.NewHealthHandler(identity), issuer, nil, log, edgeapi.RouterConfig{})
	})
}
