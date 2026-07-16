package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
)

func authErrorOutcome(err error) diagnostic.AuthFailure {
	switch {
	case errors.Is(err, authflow.ErrInvalidRegistration):
		return diagnostic.AuthInvalidRegistration
	case errors.Is(err, authflow.ErrInvalidCredentials):
		return diagnostic.AuthInvalidCredentials
	default:
		return diagnostic.AuthDependencyUnavailable
	}
}

func writeAuthJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeIdentityError(w http.ResponseWriter, request *http.Request, status int, code, message string) {
	type body struct {
		Code      string `json:"code"`
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}
	setPrivateNoStore(w)
	writeAuthJSON(w, status, body{Code: code, Error: message, RequestID: chimiddleware.GetReqID(request.Context())})
}

func setPrivateNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, private")
}
