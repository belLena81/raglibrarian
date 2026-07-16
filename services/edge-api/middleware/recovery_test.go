package middleware_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	qmiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

type delayedPanicDiagnostics struct {
	recorder *diagnostic.Recorder
	delay    time.Duration
}

func (d delayedPanicDiagnostics) PanicRecovered(request *http.Request) {
	time.Sleep(d.delay)
	d.recorder.PanicRecovered(request)
}

func (d delayedPanicDiagnostics) RequestCompleted(
	request *http.Request,
	status int,
	outcome diagnostic.RequestOutcome,
	duration time.Duration,
	responseBytes int,
) {
	d.recorder.RequestCompleted(request, status, outcome, duration, responseBytes)
}

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

func TestRecoveryHandlesNonComparablePanicValues(t *testing.T) {
	const canary = "panic-secret-canary"
	tests := []struct {
		name  string
		value any
	}{
		{name: "slice", value: []string{canary}},
		{name: "map", value: map[string]string{"secret": canary}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log, logs := newObservedLogger()
			handler := qmiddleware.RequestID(
				qmiddleware.Recovery(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					panic(test.value)
				})),
			)
			recorder := httptest.NewRecorder()

			assert.NotPanics(t, func() {
				handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))
			})

			assert.Equal(t, http.StatusInternalServerError, recorder.Code)
			assert.NotContains(t, recorder.Body.String(), canary)
			require.Equal(t, 1, logs.Len())
			assert.NotContains(t, logs.All()[0].Message+fieldsToString(logs.All()[0].ContextMap()), canary)
		})
	}
}

func TestRecoveryAndCompletionLoggerShareRequestIDAndStatus(t *testing.T) {
	recorder, logs := newObservedLogger()
	const recoveryDelay = 15 * time.Millisecond
	log := delayedPanicDiagnostics{recorder: recorder, delay: recoveryDelay}
	router := chi.NewRouter()
	router.Use(qmiddleware.RequestID)
	router.Use(qmiddleware.RequestLogger(log))
	router.Use(qmiddleware.Recovery(log))
	router.Get("/panic", func(http.ResponseWriter, *http.Request) {
		panic("sensitive-panic-value")
	})
	response := httptest.NewRecorder()

	request := httptest.NewRequest(http.MethodGet, "/panic?sensitive=query-canary", nil)
	request.Header.Set("Authorization", "Bearer credential-canary")
	request.Header.Set("Cookie", "refresh_token=credential-canary")
	router.ServeHTTP(response, request)

	require.Equal(t, 2, logs.Len())
	panicEntry := logs.All()[0]
	completionEntry := logs.All()[1]
	assert.Equal(t, "http.panic.recovered", panicEntry.Message)
	assert.Equal(t, "http.request.completed", completionEntry.Message)
	assert.Equal(t, response.Header().Get("X-Request-ID"), panicEntry.ContextMap()["request_id"])
	assert.Equal(t, panicEntry.ContextMap()["request_id"], completionEntry.ContextMap()["request_id"])
	assert.EqualValues(t, http.StatusInternalServerError, completionEntry.ContextMap()["status"])
	assert.Equal(t, "server_error", completionEntry.ContextMap()["outcome"])
	assert.EqualValues(t, response.Body.Len(), completionEntry.ContextMap()["response_bytes"])
	assert.GreaterOrEqual(t, completionEntry.ContextMap()["duration_ms"], int64(recoveryDelay/time.Millisecond))
	assert.Equal(t, "/panic", completionEntry.ContextMap()["route"])
	assert.Equal(t, panicEntry.ContextMap()["route"], completionEntry.ContextMap()["route"])
	serialized := fieldsToString(panicEntry.ContextMap()) + fieldsToString(completionEntry.ContextMap())
	assert.NotContains(t, serialized, "query-canary")
	assert.NotContains(t, serialized, "credential-canary")
}

func TestRecoveryReportsCommittedPanicAsAborted(t *testing.T) {
	const responseBody = "safe partial response"
	const canary = "committed-panic-secret-canary"
	log, logs := newObservedLogger()
	router := chi.NewRouter()
	router.Use(qmiddleware.RequestID)
	router.Use(qmiddleware.RequestLogger(log))
	router.Use(qmiddleware.Recovery(log))
	router.Get("/work", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(responseBody))
		panic(canary)
	})
	recorder := httptest.NewRecorder()

	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/work", nil))
	})

	assert.Equal(t, http.StatusAccepted, recorder.Code)
	assert.Equal(t, responseBody, recorder.Body.String())
	require.Equal(t, 2, logs.Len())
	assert.Equal(t, "http.panic.recovered", logs.All()[0].Message)
	completion := logs.All()[1]
	assert.Equal(t, "http.request.completed", completion.Message)
	assert.Equal(t, "response_aborted", completion.ContextMap()["outcome"])
	assert.EqualValues(t, http.StatusAccepted, completion.ContextMap()["status"])
	assert.EqualValues(t, len(responseBody), completion.ContextMap()["response_bytes"])
	serialized := recorder.Body.String()
	for _, entry := range logs.All() {
		serialized += entry.Message + fieldsToString(entry.ContextMap())
	}
	assert.NotContains(t, serialized, canary)
}

func TestRecoveryRepanicsAbortHandlerWithoutPanicEventOrErrorBody(t *testing.T) {
	const canary = "wrapped-abort-secret-canary"
	tests := []struct {
		name       string
		panicValue any
		write      bool
		wantStatus int
		wantBytes  int
	}{
		{name: "direct uncommitted", panicValue: http.ErrAbortHandler, wantStatus: 0},
		{name: "direct committed", panicValue: http.ErrAbortHandler, write: true, wantStatus: http.StatusAccepted, wantBytes: len("partial")},
		{name: "wrapped uncommitted", panicValue: fmt.Errorf("%s: %w", canary, http.ErrAbortHandler), wantStatus: 0},
		{name: "wrapped committed", panicValue: fmt.Errorf("%s: %w", canary, http.ErrAbortHandler), write: true, wantStatus: http.StatusAccepted, wantBytes: len("partial")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log, logs := newObservedLogger()
			router := chi.NewRouter()
			router.Use(qmiddleware.RequestID)
			router.Use(qmiddleware.RequestLogger(log))
			router.Use(qmiddleware.Recovery(log))
			router.Get("/abort", func(w http.ResponseWriter, _ *http.Request) {
				if test.write {
					w.WriteHeader(http.StatusAccepted)
					_, _ = w.Write([]byte("partial"))
				}
				panic(test.panicValue)
			})
			recorder := httptest.NewRecorder()

			assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
				router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/abort", nil))
			})

			require.Equal(t, 1, logs.Len())
			completion := logs.All()[0]
			assert.Equal(t, "http.request.completed", completion.Message)
			assert.Equal(t, "response_aborted", completion.ContextMap()["outcome"])
			assert.EqualValues(t, test.wantStatus, completion.ContextMap()["status"])
			assert.EqualValues(t, test.wantBytes, completion.ContextMap()["response_bytes"])
			assert.NotContains(t, recorder.Body.String(), "internal server error")
			assert.NotContains(t, recorder.Body.String()+completion.Message+fieldsToString(completion.ContextMap()), canary)
		})
	}
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

	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/work", nil))
	})

	assert.Equal(t, http.StatusAccepted, recorder.Code)
	assert.Equal(t, responseBody, recorder.Body.String())
	require.Equal(t, 1, logs.Len())
	assert.NotContains(t, fieldsToString(logs.All()[0].ContextMap()), "sensitive-panic-value")
}

func TestRecoveryRejectsTypedNilDiagnostics(t *testing.T) {
	var diagnostics *diagnostic.Recorder
	assert.Panics(t, func() { qmiddleware.Recovery(diagnostics) })
}
