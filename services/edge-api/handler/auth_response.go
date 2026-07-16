package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
)

func authErrToStatus(err error) int {
	switch {
	case errors.Is(err, authflow.ErrEmailTaken):
		return http.StatusConflict
	case errors.Is(err, authflow.ErrInvalidRegistration):
		return http.StatusUnprocessableEntity
	case errors.Is(err, authflow.ErrInvalidCredentials):
		return http.StatusUnauthorized
	default:
		return http.StatusServiceUnavailable
	}
}

func sanitiseAuthError(err error) string {
	switch {
	case errors.Is(err, authflow.ErrEmailTaken):
		return "email is already registered"
	case errors.Is(err, authflow.ErrInvalidRegistration):
		return "email or password is invalid"
	case errors.Is(err, authflow.ErrInvalidCredentials):
		return "invalid credentials"
	default:
		return "authentication service unavailable"
	}
}

func authErrorOutcome(err error) string {
	switch {
	case errors.Is(err, authflow.ErrEmailTaken):
		return "email_conflict"
	case errors.Is(err, authflow.ErrInvalidRegistration):
		return "invalid_registration"
	case errors.Is(err, authflow.ErrInvalidCredentials):
		return "invalid_credentials"
	default:
		return "dependency_unavailable"
	}
}

func writeAuthJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAuthError(w http.ResponseWriter, status int, message string) {
	type body struct {
		Error string `json:"error"`
	}
	writeAuthJSON(w, status, body{Error: message})
}
