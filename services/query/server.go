// Package query is the entry point for the query service.
// It wires together the chi router, middleware, and handlers.
// All long-lived dependencies are passed in (constructor injection) so the
// server is testable without starting a real TCP listener.
package query

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/services/query/handler"
	"github.com/belLena81/raglibrarian/services/query/middleware"
)

// NewRouter builds and returns the chi router for the query service.
// Keeping routing separate from main() lets integration tests create the
// router directly without listening on a port.
//
// Middleware order matters:
//  1. RealIP    — fix r.RemoteAddr before anything logs it
//  2. RequestID — inject correlation ID before the logger reads it
//  3. RequestLogger — log after ID is set, before the handler runs
//  4. Recoverer — innermost so it catches handler panics and logs them
func NewRouter(qh *handler.QueryHandler, log *zap.Logger) http.Handler {
	r := chi.NewRouter()

	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.RequestID)
	r.Use(middleware.RequestLogger(log))
	r.Use(chimiddleware.Recoverer)

	r.Get("/healthz", qh.Health)

	r.Route("/query", func(r chi.Router) {
		r.Post("/", qh.Query)
	})

	return r
}
