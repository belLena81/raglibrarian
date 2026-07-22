// Package edgeapi wires the public HTTP boundary.
package edgeapi

import (
	"net/http"
	"net/netip"
	"time"

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
type RouterConfig struct {
	TrustedProxyCIDRs     []netip.Prefix
	PublicOrigin          string
	EnforceBrowserOrigin  bool
	QueryRateLimit        int
	QueryRateWindow       time.Duration
	QueryRateMaxKeys      int
	QueryConcurrency      int
	BookUploadRateLimit   int
	BookUploadRateWindow  time.Duration
	BookUploadRateMaxKeys int
}

// NewRouter wires all public routes and mandatory authentication dependencies.
func NewRouter(
	query *handler.QueryHandler,
	authHandler *handler.AuthHandler,
	health *handler.HealthHandler,
	setup *handler.SetupHandler,
	admin *handler.AdminHandler,
	verifier TokenVerifier,
	sessions middleware.SessionValidator,
	diagnostics *diagnostic.Recorder,
	config RouterConfig,
	books ...*handler.BooksHandler,
) http.Handler {
	if query == nil || authHandler == nil || health == nil || setup == nil || admin == nil || verifier == nil || sessions == nil || diagnostics == nil {
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
	router.Use(middleware.BrowserMutationGuard(config.PublicOrigin, config.EnforceBrowserOrigin))

	router.Get("/healthz", health.Live)
	router.Get("/readyz", health.Ready)
	router.Route("/auth", func(router chi.Router) {
		registrationLimit := middleware.FixedWindowRateLimit(20, time.Hour, 10000)
		verificationLimit := middleware.FixedWindowRateLimit(30, time.Hour, 10000)
		loginLimit := middleware.FixedWindowRateLimit(30, time.Minute, 10000)
		resendLimit := middleware.FixedWindowRateLimit(5, time.Hour, 10000)
		resetRequestLimit := middleware.FixedWindowRateLimit(5, time.Hour, 10000)
		resetVerifyLimit := middleware.FixedWindowRateLimit(5, time.Hour, 10000)
		resetCompleteLimit := middleware.FixedWindowRateLimit(5, time.Hour, 10000)
		router.Group(func(router chi.Router) {
			router.Use(registrationLimit)
			router.Post("/register", authHandler.Register)
		})
		router.Group(func(router chi.Router) {
			router.Use(verificationLimit)
			router.Post("/verify-email", authHandler.VerifyEmail)
		})
		router.Group(func(router chi.Router) {
			router.Use(loginLimit)
			router.Post("/login", authHandler.Login)
		})
		router.Group(func(router chi.Router) {
			router.Use(resendLimit)
			router.Post("/verification/resend", authHandler.ResendVerification)
		})
		router.Group(func(router chi.Router) {
			router.Use(resetRequestLimit)
			router.Post("/password-reset/request", authHandler.RequestPasswordReset)
		})
		router.Group(func(router chi.Router) {
			router.Use(resetVerifyLimit)
			router.Post("/password-reset/verify", authHandler.VerifyPasswordReset)
		})
		router.Group(func(router chi.Router) {
			router.Use(resetCompleteLimit)
			router.Post("/password-reset/complete", authHandler.CompletePasswordReset)
		})
		router.Post("/refresh", authHandler.Refresh)
		router.Group(func(router chi.Router) {
			router.Use(middleware.Authenticator(verifier, sessions, diagnostics))
			router.Get("/me", authHandler.Me)
			router.Post("/logout", authHandler.Logout)
		})
	})
	router.Route("/setup", func(router chi.Router) {
		router.Get("/status", setup.Status)
		router.Group(func(router chi.Router) {
			router.Use(middleware.FixedWindowRateLimit(5, 15*time.Minute, 1000))
			router.Post("/admin", setup.CreateAdmin)
		})
	})
	router.Route("/admin", func(router chi.Router) {
		router.Use(middleware.Authenticator(verifier, sessions, diagnostics))
		router.Use(middleware.RequireRole(auth.RoleAdmin))
		router.Get("/users/pending", admin.ListPending)
		router.Post("/users/approve", admin.Approve)
		router.Post("/users/reject", admin.Reject)
		router.Get("/events", admin.Events)
	})
	router.Group(func(router chi.Router) {
		router.Use(middleware.Authenticator(verifier, sessions, diagnostics))
		router.Use(middleware.FixedWindowPrincipalRateLimit(queryRateLimit(config.QueryRateLimit), queryRateWindow(config.QueryRateWindow), queryRateMaxKeys(config.QueryRateMaxKeys)))
		router.Use(middleware.BoundedConcurrency(queryConcurrency(config.QueryConcurrency)))
		router.Post("/query", query.Query)
		router.Route("/query", func(router chi.Router) { router.Post("/", query.Query) })
	})
	if len(books) == 1 && books[0] != nil {
		booksHandler := books[0]
		router.Route("/books", func(router chi.Router) {
			router.Use(middleware.Authenticator(verifier, sessions, diagnostics))
			router.Get("/", booksHandler.List)
			router.Get("/events", booksHandler.Events)
			router.Get("/{book_id}", booksHandler.Get)
			router.Group(func(router chi.Router) {
				router.Use(middleware.RequireAnyRole(auth.RoleLibrarian, auth.RoleAdmin))
				router.Use(middleware.FixedWindowRateLimit(
					bookUploadRateLimit(config.BookUploadRateLimit),
					bookUploadRateWindow(config.BookUploadRateWindow),
					bookUploadRateMaxKeys(config.BookUploadRateMaxKeys),
				))
				router.Use(middleware.UploadDeadline)
				router.Post("/", booksHandler.Upload)
			})
		})
	}
	return router
}

func queryRateLimit(value int) int {
	if value > 0 {
		return value
	}
	return 30
}

func queryRateWindow(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return time.Minute
}

func queryRateMaxKeys(value int) int {
	if value > 0 {
		return value
	}
	return 10000
}

func queryConcurrency(value int) int {
	if value > 0 {
		return value
	}
	return 8
}

func bookUploadRateLimit(value int) int {
	if value > 0 {
		return value
	}
	return 20
}

func bookUploadRateWindow(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return time.Hour
}

func bookUploadRateMaxKeys(value int) int {
	if value > 0 {
		return value
	}
	return 10000
}
