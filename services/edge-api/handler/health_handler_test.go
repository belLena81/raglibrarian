package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

type readiness struct{ err error }

func (r readiness) CheckReady(context.Context) error { return r.err }

type dependencyReadinessError struct {
	dependency string
}

func (err dependencyReadinessError) Error() string { return "dependency unavailable" }
func (err dependencyReadinessError) DependencyName() string {
	return err.dependency
}

func TestHealthSeparatesLivenessAndReadiness(t *testing.T) {
	h := handler.NewHealthHandler(readiness{err: errors.New("identity down")})
	live := httptest.NewRecorder()
	h.Live(live, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusOK, live.Code)
	ready := httptest.NewRecorder()
	h.Ready(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, ready.Code)
	var readyResponse map[string]string
	assert.NoError(t, json.Unmarshal(ready.Body.Bytes(), &readyResponse))
	assert.Equal(t, "unavailable", readyResponse["status"])
	assert.Equal(t, "service unavailable", readyResponse["message"])

	h = handler.NewHealthHandler(readiness{err: dependencyReadinessError{dependency: "identity"}})
	ready = httptest.NewRecorder()
	h.Ready(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, ready.Code)
	assert.NoError(t, json.Unmarshal(ready.Body.Bytes(), &readyResponse))
	assert.Equal(t, "identity", readyResponse["dependency"])
}
