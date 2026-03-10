// Package query wires the chi router for the query service.
// Public routes: /healthz, /auth/register, /auth/login.
// Protected routes (PASETO token required): /auth/me, /auth/logout, /query/*.
package query

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/query/handler"
	"github.com/belLena81/raglibrarian/services/query/middleware"
)

// NewRouter builds and returns the chi router with all routes and middleware wired.
func NewRouter(
	qh *handler.QueryHandler,
	ah *handler.AuthHandler,
	issuer *auth.Issuer,
	log *zap.Logger,
) http.Handler {
	r := chi.NewRouter()

	// Global middleware (outermost to innermost):
	// RealIP → RequestID → RequestLogger → Recoverer
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.RequestID)
	r.Use(middleware.RequestLogger(log))
	r.Use(chimiddleware.Recoverer)

	r.Get("/healthz", qh.Health)

	r.Route("/auth", func(r chi.Router) {
		r.Post("/register", ah.Register)
		r.Post("/login", ah.Login)

		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticator(issuer, log))
			r.Get("/me", ah.Me)
			r.Post("/logout", ah.Logout)
		})
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.Authenticator(issuer, log))

		r.Route("/query", func(r chi.Router) {
			r.Post("/", qh.Query)
		})
	})

	return r
}
