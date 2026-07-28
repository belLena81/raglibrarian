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
		registrationLimit := middleware.FixedWindowRateLimit(authRegisterRateLimit(config.AuthRegisterRateLimit), authRegisterRateWindow(config.AuthRegisterRateWindow), authRegisterRateMaxKeys(config.AuthRegisterRateMaxKeys))
		verificationLimit := middleware.FixedWindowRateLimit(authVerifyEmailRateLimit(config.AuthVerifyEmailRateLimit), authVerifyEmailRateWindow(config.AuthVerifyEmailRateWindow), authVerifyEmailRateMaxKeys(config.AuthVerifyEmailRateMaxKeys))
		loginLimit := middleware.FixedWindowRateLimit(authLoginRateLimit(config.AuthLoginRateLimit), authLoginRateWindow(config.AuthLoginRateWindow), authLoginRateMaxKeys(config.AuthLoginRateMaxKeys))
		resendLimit := middleware.FixedWindowRateLimit(authResendVerificationRateLimit(config.AuthResendVerificationRateLimit), authResendVerificationRateWindow(config.AuthResendVerificationRateWindow), authResendVerificationRateMaxKeys(config.AuthResendVerificationRateMaxKeys))
		resetRequestLimit := middleware.FixedWindowRateLimit(authPasswordResetRequestRateLimit(config.AuthPasswordResetRequestRateLimit), authPasswordResetRequestRateWindow(config.AuthPasswordResetRequestRateWindow), authPasswordResetRequestRateMaxKeys(config.AuthPasswordResetRequestRateMaxKeys))
		resetVerifyLimit := middleware.FixedWindowRateLimit(authPasswordResetVerifyRateLimit(config.AuthPasswordResetVerifyRateLimit), authPasswordResetVerifyRateWindow(config.AuthPasswordResetVerifyRateWindow), authPasswordResetVerifyRateMaxKeys(config.AuthPasswordResetVerifyRateMaxKeys))
		resetCompleteLimit := middleware.FixedWindowRateLimit(authPasswordResetCompleteRateLimit(config.AuthPasswordResetCompleteRateLimit), authPasswordResetCompleteRateWindow(config.AuthPasswordResetCompleteRateWindow), authPasswordResetCompleteRateMaxKeys(config.AuthPasswordResetCompleteRateMaxKeys))
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
				setupAdminRateLimit(config.SetupAdminRateLimit),
				setupAdminRateWindow(config.SetupAdminRateWindow),
				setupAdminRateMaxKeys(config.SetupAdminRateMaxKeys),
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
				router.Group(func(uploadRouter chi.Router) {
					uploadRouter.Use(middleware.FixedWindowRateLimit(
						bookUploadRateLimit(config.BookUploadRateLimit),
						bookUploadRateWindow(config.BookUploadRateWindow),
						bookUploadRateMaxKeys(config.BookUploadRateMaxKeys),
					))
					uploadRouter.Use(middleware.UploadDeadlineWithBudget(bookUploadDeadline(config.BookUploadDeadline)))
					uploadRouter.Post("/", booksHandler.Upload)
				})
				router.Post("/{book_id}/reindex", booksHandler.Reindex)
				router.Delete("/{book_id}", booksHandler.Delete)
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

func authRegisterRateLimit(value int) int {
	if value > 0 {
		return value
	}
	return 20
}

func authRegisterRateWindow(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return time.Hour
}

func authRegisterRateMaxKeys(value int) int {
	if value > 0 {
		return value
	}
	return 10000
}

func authVerifyEmailRateLimit(value int) int {
	if value > 0 {
		return value
	}
	return 30
}

func authVerifyEmailRateWindow(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return time.Hour
}

func authVerifyEmailRateMaxKeys(value int) int {
	if value > 0 {
		return value
	}
	return 10000
}

func authLoginRateLimit(value int) int {
	if value > 0 {
		return value
	}
	return 30
}

func authLoginRateWindow(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return time.Minute
}

func authLoginRateMaxKeys(value int) int {
	if value > 0 {
		return value
	}
	return 10000
}

func authResendVerificationRateLimit(value int) int {
	if value > 0 {
		return value
	}
	return 5
}

func authResendVerificationRateWindow(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return time.Hour
}

func authResendVerificationRateMaxKeys(value int) int {
	if value > 0 {
		return value
	}
	return 10000
}

func authPasswordResetRequestRateLimit(value int) int {
	if value > 0 {
		return value
	}
	return 5
}

func authPasswordResetRequestRateWindow(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return time.Hour
}

func authPasswordResetRequestRateMaxKeys(value int) int {
	if value > 0 {
		return value
	}
	return 10000
}

func authPasswordResetVerifyRateLimit(value int) int {
	if value > 0 {
		return value
	}
	return 5
}

func authPasswordResetVerifyRateWindow(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return time.Hour
}

func authPasswordResetVerifyRateMaxKeys(value int) int {
	if value > 0 {
		return value
	}
	return 10000
}

func authPasswordResetCompleteRateLimit(value int) int {
	if value > 0 {
		return value
	}
	return 5
}

func authPasswordResetCompleteRateWindow(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return time.Hour
}

func authPasswordResetCompleteRateMaxKeys(value int) int {
	if value > 0 {
		return value
	}
	return 10000
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

func setupAdminRateLimit(value int) int {
	if value > 0 {
		return value
	}
	return 5
}

func setupAdminRateWindow(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return 15 * time.Minute
}

func setupAdminRateMaxKeys(value int) int {
	if value > 0 {
		return value
	}
	return 1000
}

func bookUploadDeadline(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return 2*time.Minute + 10*time.Second
}
