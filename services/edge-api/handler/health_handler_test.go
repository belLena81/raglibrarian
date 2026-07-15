package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

type readiness struct{ err error }

func (r readiness) CheckReady(context.Context) error { return r.err }

func TestHealthSeparatesLivenessAndReadiness(t *testing.T) {
	h := handler.NewHealthHandler(readiness{err: errors.New("identity down")})
	live := httptest.NewRecorder()
	h.Live(live, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusOK, live.Code)
	ready := httptest.NewRecorder()
	h.Ready(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, ready.Code)
}
