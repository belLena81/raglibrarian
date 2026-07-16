package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	qmiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

func TestRecoveryReturnsSanitizedErrorAndSafeEvent(t *testing.T) {
	const canary = "panic-secret-canary"
	log, logs := newObservedLogger()
	handler := qmiddleware.RequestID(
		qmiddleware.Recovery(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(canary)
		})),
	)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic/"+canary, nil))

	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Equal(t, "no-store, private", recorder.Header().Get("Cache-Control"))
	assert.NotContains(t, recorder.Body.String(), canary)

	var response struct {
		Code      string `json:"code"`
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "internal_error", response.Code)
	assert.Equal(t, "internal server error", response.Error)
	assert.Equal(t, recorder.Header().Get("X-Request-ID"), response.RequestID)

	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	assert.Equal(t, "http.panic.recovered", entry.Message)
	fields := entry.ContextMap()
	assert.Equal(t, response.RequestID, fields["request_id"])
	assert.Equal(t, "internal_panic", fields["error_code"])
	assert.Equal(t, "unmatched", fields["route"])
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), fields["stack_fingerprint"])
	assert.NotContains(t, entry.Message+fieldsToString(fields), canary)
}

func TestRecoveryAndCompletionLoggerShareRequestIDAndStatus(t *testing.T) {
	log, logs := newObservedLogger()
	router := chi.NewRouter()
	router.Use(qmiddleware.RequestID)
	router.Use(qmiddleware.Recovery(log))
	router.Use(qmiddleware.RequestLogger(log))
	router.Get("/panic", func(http.ResponseWriter, *http.Request) {
		panic("sensitive-panic-value")
	})
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))

	require.Equal(t, 2, logs.Len())
	completionEntry := logs.All()[0]
	panicEntry := logs.All()[1]
	assert.Equal(t, "http.panic.recovered", panicEntry.Message)
	assert.Equal(t, "http.request.completed", completionEntry.Message)
	assert.Equal(t, recorder.Header().Get("X-Request-ID"), panicEntry.ContextMap()["request_id"])
	assert.Equal(t, panicEntry.ContextMap()["request_id"], completionEntry.ContextMap()["request_id"])
	assert.EqualValues(t, http.StatusInternalServerError, completionEntry.ContextMap()["status"])
	assert.Equal(t, "/panic", completionEntry.ContextMap()["route"])
}

func TestRecoveryDoesNotAppendErrorBodyAfterResponseCommitted(t *testing.T) {
	const responseBody = "safe partial response"
	log, logs := newObservedLogger()
	handler := qmiddleware.RequestID(
		qmiddleware.Recovery(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(responseBody))
			panic("sensitive-panic-value")
		})),
	)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/work", nil))

	assert.Equal(t, http.StatusAccepted, recorder.Code)
	assert.Equal(t, responseBody, recorder.Body.String())
	require.Equal(t, 1, logs.Len())
	assert.NotContains(t, fieldsToString(logs.All()[0].ContextMap()), "sensitive-panic-value")
}

func TestRecoveryRejectsTypedNilDiagnostics(t *testing.T) {
	var diagnostics *diagnostic.Recorder
	assert.Panics(t, func() { qmiddleware.Recovery(diagnostics) })
}
