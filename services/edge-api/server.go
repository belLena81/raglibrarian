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
	TrustedProxyCIDRs                    []netip.Prefix
	PublicOrigin                         string
	EnforceBrowserOrigin                 bool
	QueryRateLimit                       int
	QueryRateWindow                      time.Duration
	QueryRateMaxKeys                     int
	QueryConcurrency                     int
	AuthRegisterRateLimit                int
	AuthRegisterRateWindow               time.Duration
	AuthRegisterRateMaxKeys              int
	AuthVerifyEmailRateLimit             int
	AuthVerifyEmailRateWindow            time.Duration
	AuthVerifyEmailRateMaxKeys           int
	AuthLoginRateLimit                   int
	AuthLoginRateWindow                  time.Duration
	AuthLoginRateMaxKeys                 int
	AuthResendVerificationRateLimit      int
	AuthResendVerificationRateWindow     time.Duration
	AuthResendVerificationRateMaxKeys    int
	AuthPasswordResetRequestRateLimit    int
	AuthPasswordResetRequestRateWindow   time.Duration
	AuthPasswordResetRequestRateMaxKeys  int
	AuthPasswordResetVerifyRateLimit     int
	AuthPasswordResetVerifyRateWindow    time.Duration
	AuthPasswordResetVerifyRateMaxKeys   int
	AuthPasswordResetCompleteRateLimit   int
	AuthPasswordResetCompleteRateWindow  time.Duration
	AuthPasswordResetCompleteRateMaxKeys int
	SetupAdminRateLimit                  int
	SetupAdminRateWindow                 time.Duration
	SetupAdminRateMaxKeys                int
	BookUploadRateLimit                  int
	BookUploadRateWindow                 time.Duration
	BookUploadRateMaxKeys                int
	BookUploadDeadline                   time.Duration
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
	if err := validateRouterConfig(config); err != nil {
		panic(err)
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
		registrationLimit := middleware.FixedWindowRateLimit(config.AuthRegisterRateLimit, config.AuthRegisterRateWindow, config.AuthRegisterRateMaxKeys)
		verificationLimit := middleware.FixedWindowRateLimit(config.AuthVerifyEmailRateLimit, config.AuthVerifyEmailRateWindow, config.AuthVerifyEmailRateMaxKeys)
		loginLimit := middleware.FixedWindowRateLimit(config.AuthLoginRateLimit, config.AuthLoginRateWindow, config.AuthLoginRateMaxKeys)
		resendLimit := middleware.FixedWindowRateLimit(config.AuthResendVerificationRateLimit, config.AuthResendVerificationRateWindow, config.AuthResendVerificationRateMaxKeys)
		resetRequestLimit := middleware.FixedWindowRateLimit(config.AuthPasswordResetRequestRateLimit, config.AuthPasswordResetRequestRateWindow, config.AuthPasswordResetRequestRateMaxKeys)
		resetVerifyLimit := middleware.FixedWindowRateLimit(config.AuthPasswordResetVerifyRateLimit, config.AuthPasswordResetVerifyRateWindow, config.AuthPasswordResetVerifyRateMaxKeys)
		resetCompleteLimit := middleware.FixedWindowRateLimit(config.AuthPasswordResetCompleteRateLimit, config.AuthPasswordResetCompleteRateWindow, config.AuthPasswordResetCompleteRateMaxKeys)
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
			router.Use(middleware.FixedWindowRateLimit(
				config.SetupAdminRateLimit,
				config.SetupAdminRateWindow,
				config.SetupAdminRateMaxKeys,
			))
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
		router.Use(middleware.FixedWindowPrincipalRateLimit(config.QueryRateLimit, config.QueryRateWindow, config.QueryRateMaxKeys))
		router.Use(middleware.BoundedConcurrency(config.QueryConcurrency))
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
				router.Group(func(uploadRouter chi.Router) {
					uploadRouter.Use(middleware.FixedWindowRateLimit(
						config.BookUploadRateLimit,
						config.BookUploadRateWindow,
						config.BookUploadRateMaxKeys,
					))
					uploadRouter.Use(middleware.UploadDeadlineWithBudget(config.BookUploadDeadline))
					uploadRouter.Post("/", booksHandler.Upload)
				})
				router.Post("/{book_id}/reindex", booksHandler.Reindex)
				router.Delete("/{book_id}", booksHandler.Delete)
			})
		})
	}
	return router
}

func validateRouterConfig(config RouterConfig) error {
	switch {
	case config.QueryRateLimit <= 0,
		config.QueryRateWindow <= 0,
		config.QueryRateMaxKeys <= 0,
		config.QueryConcurrency <= 0,
		config.AuthRegisterRateLimit <= 0,
		config.AuthRegisterRateWindow <= 0,
		config.AuthRegisterRateMaxKeys <= 0,
		config.AuthVerifyEmailRateLimit <= 0,
		config.AuthVerifyEmailRateWindow <= 0,
		config.AuthVerifyEmailRateMaxKeys <= 0,
		config.AuthLoginRateLimit <= 0,
		config.AuthLoginRateWindow <= 0,
		config.AuthLoginRateMaxKeys <= 0,
		config.AuthResendVerificationRateLimit <= 0,
		config.AuthResendVerificationRateWindow <= 0,
		config.AuthResendVerificationRateMaxKeys <= 0,
		config.AuthPasswordResetRequestRateLimit <= 0,
		config.AuthPasswordResetRequestRateWindow <= 0,
		config.AuthPasswordResetRequestRateMaxKeys <= 0,
		config.AuthPasswordResetVerifyRateLimit <= 0,
		config.AuthPasswordResetVerifyRateWindow <= 0,
		config.AuthPasswordResetVerifyRateMaxKeys <= 0,
		config.AuthPasswordResetCompleteRateLimit <= 0,
		config.AuthPasswordResetCompleteRateWindow <= 0,
		config.AuthPasswordResetCompleteRateMaxKeys <= 0,
		config.SetupAdminRateLimit <= 0,
		config.SetupAdminRateWindow <= 0,
		config.SetupAdminRateMaxKeys <= 0,
		config.BookUploadRateLimit <= 0,
		config.BookUploadRateWindow <= 0,
		config.BookUploadRateMaxKeys <= 0,
		config.BookUploadDeadline <= 0:
		return http.ErrNotSupported
	default:
		return nil
	}
}
