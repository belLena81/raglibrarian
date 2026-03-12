// Package query wires the chi router for the query service.
//
// Public routes:
//
//	GET  /healthz
//	POST /auth/register
//	POST /auth/login
//
// Authenticated routes (any valid PASETO token):
//
//	GET  /auth/me
//	POST /auth/logout
//	POST /query/
//
// Librarian/Admin routes (RoleLibrarian or above):
//
//	POST   /admin/books
//	GET    /admin/books
//	GET    /admin/books/{id}
//	DELETE /admin/books/{id}
//	POST   /admin/books/{id}/reindex
package query

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/query/handler"
	"github.com/belLena81/raglibrarian/services/query/middleware"
)

// NewRouter builds and returns the chi router with all routes and middleware wired.
func NewRouter(
	qh *handler.QueryHandler,
	ah *handler.AuthHandler,
	bh *handler.BookHandler,
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

	// ── Auth (public + authenticated) ─────────────────────────────────────
	r.Route("/auth", func(r chi.Router) {
		r.Post("/register", ah.Register)
		r.Post("/login", ah.Login)

		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticator(issuer, log))
			r.Get("/me", ah.Me)
			r.Post("/logout", ah.Logout)
		})
	})

	// ── Query (authenticated readers and above) ───────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(middleware.Authenticator(issuer, log))
		r.Route("/query", func(r chi.Router) {
			r.Post("/", qh.Query)
		})
	})

	// ── Admin — book management (librarian and above) ─────────────────────
	// Authenticator runs first to produce 401 on missing/invalid token before
	// RequireMinRole can produce 403. This ordering prevents leaking the
	// existence of admin routes to unauthenticated callers.
	r.Group(func(r chi.Router) {
		r.Use(middleware.Authenticator(issuer, log))
		r.Use(middleware.RequireMinRole(domain.RoleLibrarian))

		r.Route("/admin/books", func(r chi.Router) {
			r.Post("/", bh.AddBook)
			r.Get("/", bh.ListBooks)
			r.Get("/{id}", bh.GetBook)
			r.Delete("/{id}", bh.RemoveBook)
			r.Post("/{id}/reindex", bh.TriggerReindex)
		})
	})

	return r
}
