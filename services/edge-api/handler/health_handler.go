package handler

import (
	"context"
	"net/http"
)

// ReadinessChecker verifies that an essential dependency can serve requests.
type ReadinessChecker interface{ CheckReady(context.Context) error }

// HealthHandler owns liveness and dependency-aware readiness endpoints.
type HealthHandler struct{ readiness ReadinessChecker }

// NewHealthHandler constructs health endpoints with mandatory readiness.
func NewHealthHandler(readiness ReadinessChecker) *HealthHandler {
	if readiness == nil {
		panic("handler: ReadinessChecker must not be nil")
	}
	return &HealthHandler{readiness: readiness}
}

// Live reports process liveness without consulting dependencies.
func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Ready reports whether the Identity dependency is serving.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	if h.readiness.CheckReady(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
