// Package handler contains the HTTP transport layer for auth endpoints.
// Decodes requests → calls application service → encodes responses.
// Zero business logic lives here.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/yourname/raglibrarian/services/query/internal/application/auth"
	"github.com/yourname/raglibrarian/services/query/internal/domain/user"
	"github.com/yourname/raglibrarian/services/query/internal/transport/http/middleware"
)

type Handler struct {
	svc *auth.Service
}

func New(svc *auth.Service) *Handler {
	return &Handler{svc: svc}
}

// ── Request/Response types ────────────────────────────────────────────────────

type registerReq struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token"`
}

type tokenPairResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type authResp struct {
	UserID string        `json:"user_id"`
	Name   string        `json:"name"`
	Email  string        `json:"email"`
	Role   string        `json:"role"`
	Tokens tokenPairResp `json:"tokens,omitempty"`
}

type meResp struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type errResp struct {
	Error string `json:"error"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Register godoc
// POST /auth/register
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	out, err := h.svc.Register(r.Context(), auth.RegisterInput{
		Name: req.Name, Email: req.Email, Password: req.Password,
	})
	if err != nil {
		writeAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, authResp{
		UserID: out.UserID.String(),
		Name:   out.Name,
		Email:  out.Email,
		Role:   string(out.Role),
		Tokens: tokenPairResp{
			AccessToken:  out.Tokens.AccessToken,
			RefreshToken: out.Tokens.RefreshToken,
		},
	})
}

// Login godoc
// POST /auth/login
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	out, err := h.svc.Login(r.Context(), auth.LoginInput{Email: req.Email, Password: req.Password})
	if err != nil {
		// Map both to 401 — never leak which field was wrong
		if errors.Is(err, user.ErrUserNotFound) || errors.Is(err, user.ErrInvalidPassword) {
			writeErr(w, http.StatusUnauthorized, "invalid email or password")
			return
		}
		writeErr(w, http.StatusInternalServerError, "login failed")
		return
	}
	writeJSON(w, http.StatusOK, authResp{
		UserID: out.UserID.String(),
		Name:   out.Name,
		Email:  out.Email,
		Role:   string(out.Role),
		Tokens: tokenPairResp{
			AccessToken:  out.Tokens.AccessToken,
			RefreshToken: out.Tokens.RefreshToken,
		},
	})
}

// Refresh godoc
// POST /auth/refresh
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.RefreshToken == "" {
		writeErr(w, http.StatusBadRequest, "refresh_token is required")
		return
	}
	pair, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		if errors.Is(err, auth.ErrRefreshTokenInvalid) {
			writeErr(w, http.StatusUnauthorized, "refresh token is invalid or expired")
			return
		}
		writeErr(w, http.StatusInternalServerError, "refresh failed")
		return
	}
	writeJSON(w, http.StatusOK, tokenPairResp{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
	})
}

// Logout godoc
// POST /auth/logout  (requires valid access token)
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromCtx(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if err := h.svc.Logout(r.Context(), claims.UserID); err != nil {
		writeErr(w, http.StatusInternalServerError, "logout failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Me godoc
// GET /auth/me  (requires valid access token)
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromCtx(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	out, err := h.svc.Me(r.Context(), claims.UserID)
	if err != nil {
		if errors.Is(err, user.ErrUserNotFound) {
			writeErr(w, http.StatusUnauthorized, "user no longer exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not fetch profile")
		return
	}
	writeJSON(w, http.StatusOK, meResp{
		UserID: out.UserID.String(),
		Name:   out.Name,
		Email:  out.Email,
		Role:   string(out.Role),
	})
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeAuthErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, user.ErrEmailTaken):
		writeErr(w, http.StatusConflict, "email is already registered")
	case errors.Is(err, user.ErrInvalidEmail),
		errors.Is(err, user.ErrEmptyName),
		errors.Is(err, user.ErrNameTooLong),
		errors.Is(err, auth.ErrPasswordTooShort),
		errors.Is(err, auth.ErrPasswordTooLong):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errResp{Error: msg})
}
