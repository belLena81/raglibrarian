// Package edgeapi wires the public HTTP boundary.
package edgeapi

import (
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

// TokenVerifier validates access tokens without signing capability.
type TokenVerifier interface {
	Validate(string) (auth.Claims, error)
}

// RouterConfig controls optional perimeter proxy trust.
type RouterConfig struct{ TrustedProxyCIDRs []netip.Prefix }

// NewRouter wires all public routes and mandatory authentication dependencies.
func NewRouter(
	query *handler.QueryHandler,
	authHandler *handler.AuthHandler,
	health *handler.HealthHandler,
	verifier TokenVerifier,
	sessions middleware.SessionValidator,
	diagnostics *diagnostic.Recorder,
	config RouterConfig,
) http.Handler {
	if query == nil || authHandler == nil || health == nil || verifier == nil || sessions == nil || diagnostics == nil {
		panic("edgeapi: all router dependencies are required")
	}
	router := chi.NewRouter()
	router.Use(middleware.RequestID(diagnostics))
	router.Use(middleware.RequestLogger(diagnostics))
	router.Use(middleware.Recovery(diagnostics))
	if len(config.TrustedProxyCIDRs) > 0 {
		router.Use(middleware.TrustedProxyRealIP(config.TrustedProxyCIDRs))
	}
	router.Use(middleware.SecurityHeaders)

	router.Get("/healthz", health.Live)
	router.Get("/readyz", health.Ready)
	router.Route("/auth", func(router chi.Router) {
		router.Post("/register", authHandler.Register)
		router.Post("/login", authHandler.Login)
		router.Post("/refresh", authHandler.Refresh)
		router.Group(func(router chi.Router) {
			router.Use(middleware.Authenticator(verifier, sessions, diagnostics))
			router.Get("/me", authHandler.Me)
			router.Post("/logout", authHandler.Logout)
		})
	})
	router.Group(func(router chi.Router) {
		router.Use(middleware.Authenticator(verifier, sessions, diagnostics))
		router.Post("/query", query.Query)
		router.Route("/query", func(router chi.Router) { router.Post("/", query.Query) })
	})
	return router
}
