// Package router wires all HTTP routes for the Query Service.
package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/yourname/raglibrarian/pkg/tokenverifier"
	"github.com/yourname/raglibrarian/services/query/internal/transport/http/handler"
	"github.com/yourname/raglibrarian/services/query/internal/transport/http/middleware"
)

// New builds the root http.Handler.
// verifier is the tokenverifier.Verifier — locally a *paseto.Issuer on Query Service.
func New(authHandler *handler.Handler, verifier tokenverifier.Verifier) http.Handler {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(chimw.StripSlashes)

	// ── Public auth routes ─────────────────────────────────────────────────────
	r.Post("/auth/register", authHandler.Register)
	r.Post("/auth/login",    authHandler.Login)
	r.Post("/auth/refresh",  authHandler.Refresh) // refresh token only — no access token needed

	// ── Authenticated routes ───────────────────────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(middleware.Authenticate(verifier))

		// Any authenticated user
		r.Get("/auth/me",      authHandler.Me)
		r.Post("/auth/logout", authHandler.Logout)

		// Reader and above — future: query endpoints
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireReader)
			// r.Post("/query", queryHandler.Query)
		})

		// Librarian and above — future: book management
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireLibrarian)
			// r.Post("/books", bookHandler.Upload)
		})

		// Admin only — future: dashboard + user management
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole(tokenverifier.RoleAdmin))
			// r.Get("/admin/users", adminHandler.ListUsers)
		})
	})

	return r
}
