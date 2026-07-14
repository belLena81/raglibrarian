// Package edgeapi wires the public chi router.
// Public routes: /healthz, /auth/register, /auth/login.
// Protected routes (PASETO token required): /auth/me, /auth/logout, /query/*.
package edgeapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

type tokenVerifier interface {
	Validate(string) (auth.Claims, error)
}

// NewRouter builds and returns the chi router with all routes and middleware wired.
func NewRouter(
	qh *handler.QueryHandler,
	ah *handler.AuthHandler,
	verifier tokenVerifier,
	log *zap.Logger,
) http.Handler {
	r := chi.NewRouter()

	// Global middleware (outermost to innermost):
	// RealIP → RequestID → RequestLogger → Recoverer
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.RequestID)
	r.Use(securityHeaders)
	r.Use(middleware.RequestLogger(log))
	r.Use(chimiddleware.Recoverer)

	r.Get("/healthz", qh.Health)

	r.Route("/auth", func(r chi.Router) {
		r.Post("/register", ah.Register)
		r.Post("/login", ah.Login)

		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticator(verifier, log))
			r.Get("/me", ah.Me)
			r.Post("/logout", ah.Logout)
		})
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.Authenticator(verifier, log))

		r.Route("/query", func(r chi.Router) {
			r.Post("/", qh.Query)
		})
	})

	return r
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if r.URL.Path == "/auth/login" || r.URL.Path == "/auth/register" || r.URL.Path == "/auth/logout" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
