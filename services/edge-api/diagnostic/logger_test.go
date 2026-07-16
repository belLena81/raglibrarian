package diagnostic_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
)

const validRequestID = "0123456789abcdef0123456789abcdef"

func observedRecorder() (*diagnostic.Recorder, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return diagnostic.New(zap.New(core)), logs
}

func diagnosticRequest(method, target, pattern string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	routeContext := chi.NewRouteContext()
	if pattern != "" {
		routeContext.RoutePatterns = append(routeContext.RoutePatterns, pattern)
	}
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, chimiddleware.RequestIDKey, validRequestID)
	return request.WithContext(ctx)
}

func sortedKeys(entry observer.LoggedEntry) []string {
	keys := make([]string, 0, len(entry.Context))
	for _, field := range entry.Context {
		keys = append(keys, field.Key)
	}
	sort.Strings(keys)
	return keys
}

func TestRequestCompletedUsesExactSchemaLevelsAndOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		outcome diagnostic.RequestOutcome
		level   zapcore.Level
		value   string
	}{
		{name: "success", outcome: diagnostic.RequestSuccess, level: zapcore.InfoLevel, value: "success"},
		{name: "client error", outcome: diagnostic.RequestClientError, level: zapcore.WarnLevel, value: "client_error"},
		{name: "server error", outcome: diagnostic.RequestServerError, level: zapcore.ErrorLevel, value: "server_error"},
		{name: "not implemented", outcome: diagnostic.RequestNotImplemented, level: zapcore.WarnLevel, value: "not_implemented"},
		{name: "response aborted", outcome: diagnostic.RequestResponseAborted, level: zapcore.ErrorLevel, value: "response_aborted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder, logs := observedRecorder()
			recorder.RequestCompleted(
				diagnosticRequest(http.MethodPost, "/books/subject-id", "/books/{bookID}"),
				http.StatusAccepted,
				test.outcome,
				12*time.Millisecond,
				42,
			)

			require.Equal(t, 1, logs.Len())
			entry := logs.All()[0]
			assert.Equal(t, "http.request.completed", entry.Message)
			assert.Equal(t, test.level, entry.Level)
			assert.Equal(t, test.value, entry.ContextMap()["outcome"])
			assert.Equal(t, "/books/{bookID}", entry.ContextMap()["route"])
			assert.Equal(t, []string{"duration_ms", "method", "outcome", "request_id", "response_bytes", "route", "status"}, sortedKeys(entry))
		})
	}
}

func TestSecurityAndStateEventsUseExactSchemas(t *testing.T) {
	request := diagnosticRequest(http.MethodPost, "/auth/login", "/auth/login")
	tests := []struct {
		name    string
		emit    func(*diagnostic.Recorder)
		event   string
		outcome string
		keys    []string
	}{
		{name: "token", emit: func(r *diagnostic.Recorder) { r.TokenRejected(request) }, event: "auth.token.rejected", outcome: "invalid_token", keys: []string{"outcome", "request_id"}},
		{name: "registration", emit: func(r *diagnostic.Recorder) { r.RegistrationFailed(request, diagnostic.AuthEmailConflict) }, event: "auth.register.failed", outcome: "email_conflict", keys: []string{"outcome", "request_id"}},
		{name: "login", emit: func(r *diagnostic.Recorder) { r.LoginFailed(request, diagnostic.AuthInvalidCredentials) }, event: "auth.login.failed", outcome: "invalid_credentials", keys: []string{"outcome", "request_id"}},
		{name: "query", emit: func(r *diagnostic.Recorder) { r.RetrievalUnavailable(request) }, event: "query.retrieval.unavailable", outcome: "not_implemented", keys: []string{"outcome", "request_id"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder, logs := observedRecorder()
			test.emit(recorder)
			require.Equal(t, 1, logs.Len())
			entry := logs.All()[0]
			assert.Equal(t, test.event, entry.Message)
			assert.Equal(t, test.outcome, entry.ContextMap()["outcome"])
			assert.Equal(t, test.keys, sortedKeys(entry))
		})
	}
}

func TestPanicRecoveredUsesExactSafeSchema(t *testing.T) {
	recorder, logs := observedRecorder()
	recorder.PanicRecovered(diagnosticRequest(http.MethodGet, "/panic", "/panic"))

	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	assert.Equal(t, "http.panic.recovered", entry.Message)
	assert.Equal(t, zapcore.ErrorLevel, entry.Level)
	assert.Equal(t, "internal_panic", entry.ContextMap()["error_code"])
	assert.Regexp(t, `^[0-9a-f]{32}$`, entry.ContextMap()["stack_fingerprint"])
	assert.Equal(t, []string{"error_code", "method", "request_id", "route", "stack_fingerprint"}, sortedKeys(entry))
}

func TestMalformedRequestIDProducesNoDiagnosticEvent(t *testing.T) {
	recorder, logs := observedRecorder()
	request := diagnosticRequest(http.MethodGet, "/healthz", "/healthz")
	request = request.WithContext(context.WithValue(request.Context(), chimiddleware.RequestIDKey, "client-controlled"))

	recorder.RetrievalUnavailable(request)

	assert.Zero(t, logs.Len())
}

func TestInvalidOutcomesProduceNoDiagnosticEvent(t *testing.T) {
	request := diagnosticRequest(http.MethodPost, "/auth/login", "/auth/login")
	tests := []struct {
		name string
		emit func(*diagnostic.Recorder)
	}{
		{name: "request", emit: func(r *diagnostic.Recorder) {
			r.RequestCompleted(request, http.StatusOK, diagnostic.RequestOutcome(255), 0, 0)
		}},
		{name: "registration context", emit: func(r *diagnostic.Recorder) { r.RegistrationFailed(request, diagnostic.AuthInvalidCredentials) }},
		{name: "login context", emit: func(r *diagnostic.Recorder) { r.LoginFailed(request, diagnostic.AuthEmailConflict) }},
		{name: "unknown auth", emit: func(r *diagnostic.Recorder) { r.LoginFailed(request, diagnostic.AuthFailure(255)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder, logs := observedRecorder()
			test.emit(recorder)
			assert.Zero(t, logs.Len())
		})
	}
}

func TestDiagnosticEventsDoNotContainEncodedRequestCanary(t *testing.T) {
	const canary = `Sensitive"/Canary-42`
	recorder, logs := observedRecorder()
	request := diagnosticRequest(http.MethodPost, "/items/"+url.PathEscape(canary)+"?q="+url.QueryEscape(canary), "/items/{itemID}")
	request.Method = canary
	request.Header.Set("Authorization", "Bearer "+canary)
	request.Header.Set("Cookie", "session="+canary)
	request.Header.Set("User-Agent", canary)

	recorder.RequestCompleted(request, http.StatusBadRequest, diagnostic.RequestClientError, time.Millisecond, 0)

	require.Equal(t, 1, logs.Len())
	encoded, err := json.Marshal(logs.All()[0])
	require.NoError(t, err)
	output := strings.ToLower(string(encoded))
	forms := []string{
		canary,
		strings.ToLower(canary),
		url.QueryEscape(canary),
		url.PathEscape(canary),
		base64.StdEncoding.EncodeToString([]byte(canary)),
		base64.RawStdEncoding.EncodeToString([]byte(canary)),
		base64.URLEncoding.EncodeToString([]byte(canary)),
		base64.RawURLEncoding.EncodeToString([]byte(canary)),
	}
	for _, form := range forms {
		assert.NotContains(t, output, strings.ToLower(form))
	}
}
