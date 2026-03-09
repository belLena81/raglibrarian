// Package query is the entry point for the query service.
// It wires together the chi router, middleware, and handlers.
// All long-lived dependencies are passed in (constructor injection) so the
// server is testable without starting a real TCP listener.
package query

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/belLena81/raglibrarian/services/query/handler"
)

// NewRouter builds and returns the chi router for the query service.
// Keeping routing separate from main() lets integration tests create the
// router directly without listening on a port.
func NewRouter(qh *handler.QueryHandler) http.Handler {
	r := chi.NewRouter()

	// ── Middleware stack ────────────────────────────────────────────────────
	// RealIP + RequestID are chi stdlib middlewares — zero external deps.
	// Recoverer turns a panic into a 500 instead of crashing the process.
	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	// ── Routes ──────────────────────────────────────────────────────────────
	r.Get("/healthz", qh.Health)

	r.Route("/query", func(r chi.Router) {
		r.Post("/", qh.Query)
	})

	return r
}
