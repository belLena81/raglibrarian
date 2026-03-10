// Package handler contains HTTP handlers for the query service.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/metadata/usecase"
	querymiddleware "github.com/belLena81/raglibrarian/services/query/middleware"
)

// ── DTOs ──────────────────────────────────────────────────────────────────────

// RegisterRequest is the JSON body for POST /auth/register.
type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"` // defaults to "reader" when absent
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
	uc  usecase.AuthUseCase
	log *zap.Logger
}

// NewAuthHandler constructs an AuthHandler. Panics on nil deps.
func NewAuthHandler(uc usecase.AuthUseCase, log *zap.Logger) *AuthHandler {
	if uc == nil {
		panic("handler: AuthUseCase must not be nil")
	}
	if log == nil {
		panic("handler: Logger must not be nil")
	}
	return &AuthHandler{uc: uc, log: log}
}

// Register handles POST /auth/register.
// Creates an account and issues a token in one request.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	reqID := middleware.GetReqID(r.Context())

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	role := domain.Role(req.Role)
	if role == "" {
		role = domain.RoleReader
	}

	token, user, err := h.uc.Register(r.Context(), req.Email, req.Password, role)
	if err != nil {
		h.log.Debug("register failed",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		writeAuthError(w, authErrToStatus(err), sanitiseAuthError(err))
		return
	}

	writeAuthJSON(w, http.StatusCreated, AuthResponse{
		Token: token,
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

// Logout handles POST /auth/logout.
// Returns 200 so clients can discard their token; no server-side revocation yet.
// TODO(iteration-4): add token revocation via a short-lived Redis blocklist.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	type logoutResponse struct {
		Message string `json:"message"`
	}
	writeAuthJSON(w, http.StatusOK, logoutResponse{Message: "logged out"})
}

// Login handles POST /auth/login. Returns a PASETO token on success.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	token, err := h.uc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		h.log.Debug("login failed", zap.String("request_id", middleware.GetReqID(r.Context())))
		writeAuthError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	writeAuthJSON(w, http.StatusOK, AuthResponse{Token: token})
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
