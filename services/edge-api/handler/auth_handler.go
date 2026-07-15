// Package handler contains HTTP handlers for the query service.
package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	querymiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

// ── DTOs ──────────────────────────────────────────────────────────────────────

// RegisterRequest is the JSON body for POST /auth/register.
type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
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
	Email  string `json:"email"`
	Role   string `json:"role"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

// AuthHandler handles the /auth/* routes.
type AuthHandler struct {
	uc           AuthUseCase
	log          *zap.Logger
	secureCookie bool
}

// CookieConfig controls refresh-cookie transport security.
type CookieConfig struct{ Secure bool }

// NewAuthHandler constructs an AuthHandler with explicit cookie policy.
func NewAuthHandler(uc AuthUseCase, log *zap.Logger, cookies CookieConfig) *AuthHandler {
	if uc == nil {
		panic("handler: AuthUseCase must not be nil")
	}
	if log == nil {
		panic("handler: Logger must not be nil")
	}
	return &AuthHandler{uc: uc, log: log, secureCookie: cookies.Secure}
}

// AuthUseCase is the edge-facing identity contract. Its production adapter is
// a generated gRPC client; tests use a local fake.
type AuthUseCase interface {
	Register(context.Context, string, string) (authflow.Session, error)
	Login(context.Context, string, string) (authflow.Session, error)
	Refresh(context.Context, string) (authflow.Session, error)
	Logout(context.Context, string) error
}

// Register handles POST /auth/register.
// Creates an account and issues a token in one request.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	reqID := middleware.GetReqID(r.Context())

	var req RegisterRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	tokens, err := h.uc.Register(r.Context(), req.Email, req.Password)
	if err != nil {
		h.log.Debug("register failed",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		writeAuthError(w, authErrToStatus(err), sanitiseAuthError(err))
		return
	}

	h.setRefreshCookie(w, tokens.RefreshToken)
	writeAuthJSON(w, http.StatusCreated, AuthResponse{
		Token: tokens.AccessToken,
		Role:  tokens.Role,
	})
}

// Me handles GET /auth/me.
// Uses claims only after middleware has validated the live Identity session.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims, ok := querymiddleware.ClaimsFromContext(r.Context())
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	writeAuthJSON(w, http.StatusOK, MeResponse{
		UserID: claims.UserID,
		Email:  claims.Email,
		Role:   string(claims.Role),
	})
}

// Logout handles POST /auth/logout by revoking the verified Identity session.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	claims, ok := querymiddleware.ClaimsFromContext(r.Context())
	if !ok || claims.SessionID == "" {
		writeAuthError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if err := h.uc.Logout(r.Context(), claims.SessionID); err != nil && !errors.Is(err, authflow.ErrInvalidCredentials) {
		writeAuthError(w, http.StatusServiceUnavailable, "authentication service unavailable")
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
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	tokens, err := h.uc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		h.log.Debug("login failed", zap.String("request_id", middleware.GetReqID(r.Context())))
		if errors.Is(err, authflow.ErrInvalidCredentials) {
			writeAuthError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeAuthError(w, http.StatusServiceUnavailable, "authentication service unavailable")
		return
	}

	h.setRefreshCookie(w, tokens.RefreshToken)
	writeAuthJSON(w, http.StatusOK, AuthResponse{Token: tokens.AccessToken, Role: tokens.Role})
}

// Refresh rotates the cookie-held refresh token without exposing it to script.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("refresh_token")
	if err != nil || cookie.Value == "" {
		h.clearRefreshCookie(w)
		writeAuthError(w, http.StatusUnauthorized, "invalid refresh session")
		return
	}
	tokens, err := h.uc.Refresh(r.Context(), cookie.Value)
	if err != nil {
		h.clearRefreshCookie(w)
		if errors.Is(err, authflow.ErrInvalidCredentials) {
			writeAuthError(w, http.StatusUnauthorized, "invalid refresh session")
			return
		}
		writeAuthError(w, http.StatusServiceUnavailable, "authentication service unavailable")
		return
	}
	h.setRefreshCookie(w, tokens.RefreshToken)
	writeAuthJSON(w, http.StatusOK, AuthResponse{Token: tokens.AccessToken, Role: tokens.Role})
}
