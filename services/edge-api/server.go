// Package edgeapi wires the public chi router.
// Public routes: /healthz, /auth/register, /auth/login.
// Protected routes (PASETO token required): /auth/me, /auth/logout, /query/*.
package edgeapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

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
	validators ...middleware.SessionValidator,
) http.Handler {
	return NewRouterWithTrustedProxies(qh, ah, verifier, log, nil, validators...)
}

// NewRouterWithTrustedProxies builds the router and honors forwarded client
// addresses only when the direct peer matches an explicitly trusted CIDR.
func NewRouterWithTrustedProxies(
	qh *handler.QueryHandler,
	ah *handler.AuthHandler,
	verifier tokenVerifier,
	log *zap.Logger,
	trustedProxies []netip.Prefix,
	validators ...middleware.SessionValidator,
) http.Handler {
	r := chi.NewRouter()

	// Global middleware (outermost to innermost):
	// TrustedProxyRealIP → RequestID → RequestLogger → Recoverer
	if len(trustedProxies) > 0 {
		r.Use(trustedProxyRealIP(trustedProxies))
	}
	r.Use(chimiddleware.RequestID)
	r.Use(securityHeaders)
	r.Use(middleware.RequestLogger(log))
	r.Use(chimiddleware.Recoverer)

	r.Get("/healthz", qh.Health)
	r.Get("/readyz", qh.Ready)

	r.Route("/auth", func(r chi.Router) {
		r.Post("/register", ah.Register)
		r.Post("/login", ah.Login)
		r.Post("/refresh", ah.Refresh)

		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticator(verifier, log, validators...))
			r.Get("/me", ah.Me)
			r.Post("/logout", ah.Logout)
		})
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.Authenticator(verifier, log, validators...))
		r.Post("/query", qh.Query)

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
		if strings.HasPrefix(r.URL.Path, "/auth/") || r.URL.Path == "/query" || strings.HasPrefix(r.URL.Path, "/query/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func trustedProxyRealIP(prefixes []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			peer, parseErr := netip.ParseAddr(host)
			if err == nil && parseErr == nil && addressAllowed(peer, prefixes) {
				forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
				if forwarded == "" {
					forwarded = strings.TrimSpace(r.Header.Get("X-Real-IP"))
				}
				if client, clientErr := netip.ParseAddr(forwarded); clientErr == nil {
					r.RemoteAddr = net.JoinHostPort(client.String(), "0")
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func addressAllowed(address netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
