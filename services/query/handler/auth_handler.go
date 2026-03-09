// Package handler — auth_handler.go
// Handles POST /auth/register and POST /auth/login.
// Auth lives in the query service for Iteration 2. It moves to the metadata
// gRPC service in Iteration 3 when the service split is introduced.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/metadata/usecase"
)

// ── DTOs ──────────────────────────────────────────────────────────────────────

// RegisterRequest is the JSON body for POST /auth/register.
type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// Role defaults to "reader" when absent. Explicit "admin" is accepted here
	// for bootstrapping; a production hardening pass would gate this on a
	// separate admin-only endpoint.
	Role string `json:"role"`
}

// LoginRequest is the JSON body for POST /auth/login.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// AuthResponse is returned by both /register and /login on success.
// Returning the role saves the client an extra round-trip to /me.
type AuthResponse struct {
	Token string `json:"token"`
	Role  string `json:"role"`
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
// Creates an account and immediately issues a token — no separate login needed.
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

	user, err := h.uc.Register(r.Context(), req.Email, req.Password, role)
	if err != nil {
		h.log.Debug("register failed",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		writeAuthError(w, authErrToStatus(err), sanitiseAuthError(err))
		return
	}

	// Issue a token immediately so the client can start querying without a
	// separate login round-trip.
	token, err := h.uc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		h.log.Error("failed to issue token after successful registration",
			zap.String("request_id", reqID),
			zap.Error(err),
		)
		writeAuthError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeAuthJSON(w, http.StatusCreated, AuthResponse{
		Token: token,
		Role:  string(user.Role()),
	})
}

// Login handles POST /auth/login.
// Returns a PASETO token on success; a uniform 401 on any failure.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	token, err := h.uc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		// Do NOT log the email — avoid leaking PII into log aggregators.
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
	case errors.Is(err, auth.ErrInvalidCredentials):
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}

// sanitiseAuthError returns a safe, user-facing message.
// Internal errors are never surfaced — only domain validation messages are.
func sanitiseAuthError(err error) string {
	switch {
	case errors.Is(err, domain.ErrEmailTaken):
		return "email is already registered"
	case errors.Is(err, domain.ErrInvalidEmail):
		return "email format is invalid"
	case errors.Is(err, domain.ErrInvalidRole):
		return "role must be admin or reader"
	case errors.Is(err, auth.ErrInvalidCredentials):
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
