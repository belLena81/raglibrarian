// Package handler contains HTTP handlers for the query service.
package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	querymiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

// ── DTOs ──────────────────────────────────────────────────────────────────────

// RegisterRequest is the JSON body for POST /auth/register.
type RegisterRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// VerifyEmailRequest is the JSON body for POST /auth/verify-email.
type VerifyEmailRequest struct {
	Token string `json:"token"`
}

// ResendVerificationRequest is the JSON body for POST /auth/verification/resend.
type ResendVerificationRequest struct {
	Email string `json:"email"`
}

// LoginRequest is the JSON body for POST /auth/login.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// AuthResponse is returned by /register and /login on success.
type AuthResponse struct {
	Token string `json:"token"`
	Role  string `json:"role"`
}

// MeResponse is returned by GET /auth/me.
type MeResponse struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

// AuthHandler handles the /auth/* routes.
type AuthHandler struct {
	uc           AuthUseCase
	diagnostics  authDiagnostics
	secureCookie bool
}

type authDiagnostics interface {
	RegistrationFailed(*http.Request, diagnostic.AuthFailure)
	LoginFailed(*http.Request, diagnostic.AuthFailure)
}

// CookieConfig controls refresh-cookie transport security.
type CookieConfig struct{ Secure bool }

// NewAuthHandler constructs an AuthHandler with explicit cookie policy.
func NewAuthHandler(uc AuthUseCase, diagnostics authDiagnostics, cookies CookieConfig) *AuthHandler {
	if uc == nil {
		panic("handler: AuthUseCase must not be nil")
	}
	if dependencyMissing(diagnostics) {
		panic("handler: Logger must not be nil")
	}
	return &AuthHandler{uc: uc, diagnostics: diagnostics, secureCookie: cookies.Secure}
}

// AuthUseCase is the edge-facing identity contract. Its production adapter is
// a generated gRPC client; tests use a local fake.
type AuthUseCase interface {
	Register(context.Context, string, string, string, string) error
	VerifyEmail(context.Context, string) error
	ResendVerification(context.Context, string) error
	Login(context.Context, string, string) (authflow.Session, error)
	Refresh(context.Context, string) (authflow.Session, error)
	Logout(context.Context, string) error
}

// Register handles privacy-preserving verification-required registration.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeIdentityError(w, r, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}

	err := h.uc.Register(r.Context(), req.Name, req.Email, req.Password, req.Role)
	if err != nil {
		h.diagnostics.RegistrationFailed(r, authErrorOutcome(err))
		if errors.Is(err, authflow.ErrInvalidRegistration) {
			writeIdentityError(w, r, http.StatusUnprocessableEntity, "invalid_registration", "registration is invalid")
			return
		}
		writeIdentityError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity service unavailable")
		return
	}
	setPrivateNoStore(w)
	writeAuthJSON(w, http.StatusAccepted, struct {
		Accepted bool `json:"accepted"`
	}{Accepted: true})
}

// VerifyEmail consumes an email-verification token without creating a session.
func (h *AuthHandler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req VerifyEmailRequest
	if err := decodeJSONBody(w, r, &req); err != nil || req.Token == "" {
		writeIdentityError(w, r, http.StatusBadRequest, "invalid_verification", "verification is invalid or expired")
		return
	}
	if err := h.uc.VerifyEmail(r.Context(), req.Token); err != nil {
		if errors.Is(err, authflow.ErrInvalidVerification) {
			writeIdentityError(w, r, http.StatusBadRequest, "invalid_verification", "verification is invalid or expired")
			return
		}
		writeIdentityError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity service unavailable")
		return
	}
	setPrivateNoStore(w)
	w.WriteHeader(http.StatusNoContent)
}

// ResendVerification requests a privacy-preserving verification resend.
func (h *AuthHandler) ResendVerification(w http.ResponseWriter, r *http.Request) {
	var req ResendVerificationRequest
	if err := decodeJSONBody(w, r, &req); err != nil || req.Email == "" {
		writeIdentityError(w, r, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	if err := h.uc.ResendVerification(r.Context(), req.Email); err != nil {
		writeIdentityError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity service unavailable")
		return
	}
	setPrivateNoStore(w)
	writeAuthJSON(w, http.StatusAccepted, struct {
		Accepted bool `json:"accepted"`
	}{Accepted: true})
}

// Me handles GET /auth/me.
// Uses claims only after middleware has validated the live Identity session.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	principal, ok := querymiddleware.PrincipalFromContext(r.Context())
	if !ok {
		writeIdentityError(w, r, http.StatusUnauthorized, "unauthorized", "not authenticated")
		return
	}
	writeAuthJSON(w, http.StatusOK, MeResponse{
		UserID: principal.UserID,
		Name:   principal.Name,
		Email:  principal.Email,
		Role:   principal.Role,
		Status: principal.Status,
	})
}

// Logout handles POST /auth/logout by revoking the verified Identity session.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	principal, ok := querymiddleware.PrincipalFromContext(r.Context())
	if !ok || principal.SessionID == "" {
		writeIdentityError(w, r, http.StatusUnauthorized, "unauthorized", "not authenticated")
		return
	}
	if err := h.uc.Logout(r.Context(), principal.SessionID); err != nil && !errors.Is(err, authflow.ErrInvalidCredentials) {
		writeIdentityError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity service unavailable")
		return
	}
	h.clearRefreshCookie(w)
	type logoutResponse struct {
		Message string `json:"message"`
	}
	writeAuthJSON(w, http.StatusOK, logoutResponse{Message: "logged out"})
}

// Login handles POST /auth/login. Returns a PASETO token on success.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeIdentityError(w, r, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}

	tokens, err := h.uc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		h.diagnostics.LoginFailed(r, authErrorOutcome(err))
		if errors.Is(err, authflow.ErrInvalidCredentials) {
			writeIdentityError(w, r, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
			return
		}
		writeIdentityError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity service unavailable")
		return
	}

	h.setRefreshCookie(w, tokens.RefreshToken)
	writeAuthJSON(w, http.StatusOK, AuthResponse{Token: tokens.AccessToken, Role: tokens.Role})
}

// Refresh rotates the cookie-held refresh token without exposing it to script.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(h.refreshCookieName())
	if err != nil || cookie.Value == "" {
		h.clearRefreshCookie(w)
		writeIdentityError(w, r, http.StatusUnauthorized, "invalid_session", "invalid refresh session")
		return
	}
	tokens, err := h.uc.Refresh(r.Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, authflow.ErrInvalidCredentials) {
			h.clearRefreshCookie(w)
			writeIdentityError(w, r, http.StatusUnauthorized, "invalid_session", "invalid refresh session")
			return
		}
		writeIdentityError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity service unavailable")
		return
	}
	h.setRefreshCookie(w, tokens.RefreshToken)
	writeAuthJSON(w, http.StatusOK, AuthResponse{Token: tokens.AccessToken, Role: tokens.Role})
}
