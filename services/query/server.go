// Package query wires the chi router for the query service.
// Route groups define the security boundary:
//
//	Public  — /healthz, /auth/*  (no token required)
//	Private — /query/*           (valid PASETO token required)
//
// Middleware order (outermost to innermost) is documented below and must not
// be changed without updating the comment — order has observable consequences.
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

// NewRouter builds and returns the chi router.
// All dependencies are injected so the router can be constructed in tests
// without starting a real TCP listener.
func NewRouter(
	qh *handler.QueryHandler,
	ah *handler.AuthHandler,
	issuer *auth.Issuer,
	log *zap.Logger,
) http.Handler {
	r := chi.NewRouter()

	// ── Global middleware (applied to every request) ──────────────────────
	// 1. RealIP      — rewrite RemoteAddr from X-Forwarded-For before logging
	// 2. RequestID   — inject correlation ID; must precede RequestLogger
	// 3. RequestLogger — structured access log; reads RequestID from context
	// 4. Recoverer   — convert panics to 500; innermost of the global stack
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.RequestID)
	r.Use(middleware.RequestLogger(log))
	r.Use(chimiddleware.Recoverer)

	// ── Public routes (no authentication required) ────────────────────────
	r.Get("/healthz", qh.Health)

	r.Route("/auth", func(r chi.Router) {
		r.Post("/register", ah.Register)
		r.Post("/login", ah.Login)
	})

	// ── Protected routes (valid PASETO token required) ────────────────────
	// Authenticator validates the token and stores Claims in context.
	// Any route registered inside this block requires authentication.
	r.Group(func(r chi.Router) {
		r.Use(middleware.Authenticator(issuer, log))

		r.Route("/query", func(r chi.Router) {
			r.Post("/", qh.Query)
		})
	})

	return r
}
