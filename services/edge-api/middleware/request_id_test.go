package middleware

import (
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
)

type requestIDDiagnosticsSpy struct{ failures int }

func (s *requestIDDiagnosticsSpy) RequestIDGenerationFailed() { s.failures++ }

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestRequestIDIgnoresClientValueAndGenerates128Bits(t *testing.T) {
	const clientValue = "sensitive-client-request-id"

	diagnostics := &requestIDDiagnosticsSpy{}
	var contextID string
	handler := RequestID(diagnostics)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextID = chimiddleware.GetReqID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", clientValue)

	handler.ServeHTTP(recorder, request)

	responseID := recorder.Header().Get("X-Request-ID")
	require.NotEmpty(t, responseID)
	assert.Equal(t, responseID, contextID)
	assert.NotEqual(t, clientValue, responseID)
	decoded, err := hex.DecodeString(responseID)
	require.NoError(t, err)
	assert.Len(t, decoded, 16)
	assert.Zero(t, diagnostics.failures)
}

func TestRequestIDGeneratesDifferentValues(t *testing.T) {
	handler := RequestID(&requestIDDiagnosticsSpy{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.NotEqual(t, first.Header().Get("X-Request-ID"), second.Header().Get("X-Request-ID"))
}

func TestRequestIDGenerationFailureIsLoggedAndHardened(t *testing.T) {
	const sensitiveCause = "sensitive entropy details"
	core, logs := observer.New(zapcore.DebugLevel)
	diagnostics := diagnostic.New(zap.New(core))
	downstreamCalled := false
	handler := requestID(failingReader{err: errors.New(sensitiveCause)}, diagnostics)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		downstreamCalled = true
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("Authorization", "Bearer sensitive-token")
	request.Header.Set("X-Request-ID", "client-controlled")

	handler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, "service unavailable\n", recorder.Body.String())
	assert.Equal(t, "no-store, private", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "no-referrer", recorder.Header().Get("Referrer-Policy"))
	assert.Equal(t, "DENY", recorder.Header().Get("X-Frame-Options"))
	assert.Equal(t, "default-src 'none'; frame-ancestors 'none'", recorder.Header().Get("Content-Security-Policy"))
	assert.Empty(t, recorder.Header().Get("X-Request-ID"))
	assert.False(t, downstreamCalled)
	assert.NotContains(t, recorder.Body.String(), sensitiveCause)
	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	assert.Equal(t, zapcore.ErrorLevel, entry.Level)
	assert.Equal(t, "http.request_id.failed", entry.Message)
	assert.Equal(t, map[string]any{"error_code": "request_id_generation_failed"}, entry.ContextMap())
	assert.NotContains(t, entry.ContextMap(), "request_id")
}

func TestRequestIDRejectsMissingDiagnostics(t *testing.T) {
	assert.Panics(t, func() { RequestID(nil) })

	var diagnostics *requestIDDiagnosticsSpy
	assert.Panics(t, func() { RequestID(diagnostics) })
}
