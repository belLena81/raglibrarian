// Package handler contains HTTP handlers for the query service.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
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

// NewAuthHandler constructs an AuthHandler. Panics on nil deps.
func NewAuthHandler(uc AuthUseCase, log *zap.Logger, secureCookie ...bool) *AuthHandler {
	if uc == nil {
		panic("handler: AuthUseCase must not be nil")
	}
	if log == nil {
		panic("handler: Logger must not be nil")
	}
	secure := true
	if len(secureCookie) > 0 {
		secure = secureCookie[0]
	}
	return &AuthHandler{uc: uc, log: log, secureCookie: secure}
}

// AuthUseCase is the edge-facing identity contract. Its production adapter is
// a generated gRPC client; tests use a local fake.
type AuthUseCase interface {
	Register(context.Context, string, string, domain.Role) (auth.SessionTokens, domain.User, error)
	Login(context.Context, string, string) (auth.SessionTokens, error)
	Refresh(context.Context, string) (auth.SessionTokens, error)
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

	// Public registration can only create readers. Role elevation is an
	// administrative workflow and must never be client-controlled.
	tokens, user, err := h.uc.Register(r.Context(), req.Email, req.Password, domain.RoleReader)
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
		Role:  string(user.Role()),
	})
}

// Me handles GET /auth/me.
// Returns identity from the caller's PASETO token claims — no DB call.
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
	if err := h.uc.Logout(r.Context(), claims.SessionID); err != nil && !errors.Is(err, domain.ErrInvalidCredentials) {
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
		writeAuthError(w, http.StatusUnauthorized, "invalid credentials")
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
		if errors.Is(err, domain.ErrInvalidCredentials) {
			writeAuthError(w, http.StatusUnauthorized, "invalid refresh session")
			return
		}
		writeAuthError(w, http.StatusServiceUnavailable, "authentication service unavailable")
		return
	}
	h.setRefreshCookie(w, tokens.RefreshToken)
	writeAuthJSON(w, http.StatusOK, AuthResponse{Token: tokens.AccessToken, Role: tokens.Role})
}

func (h *AuthHandler) setRefreshCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{Name: "refresh_token", Value: value, Path: "/auth", HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: 60 * 60 * 24 * 30})
}

func (h *AuthHandler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "refresh_token", Value: "", Path: "/auth", HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// ── Error mapping ─────────────────────────────────────────────────────────────

func authErrToStatus(err error) int {
	switch {
	case errors.Is(err, domain.ErrEmailTaken):
		return http.StatusConflict
	case errors.Is(err, domain.ErrInvalidEmail),
		errors.Is(err, domain.ErrEmptyEmail),
		errors.Is(err, domain.ErrInvalidRole):
		return http.StatusUnprocessableEntity
	case errors.Is(err, domain.ErrInvalidCredentials):
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}

func sanitiseAuthError(err error) string {
	switch {
	case errors.Is(err, domain.ErrEmailTaken):
		return "email is already registered"
	case errors.Is(err, domain.ErrInvalidEmail):
		return "email format is invalid"
	case errors.Is(err, domain.ErrInvalidRole):
		return "role must be admin or reader"
	case errors.Is(err, domain.ErrInvalidCredentials):
		return "invalid credentials"
	default:
		return "internal server error"
	}
}

func writeAuthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	type errBody struct {
		Error string `json:"error"`
	}
	writeAuthJSON(w, status, errBody{Error: msg})
}
