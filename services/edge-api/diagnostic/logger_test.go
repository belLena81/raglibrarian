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

func observedFatalRecorder() (*diagnostic.Recorder, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	log := zap.New(core, zap.WithFatalHook(zapcore.WriteThenGoexit))
	return diagnostic.New(log), logs
}

func invokeFatal(t *testing.T, emit func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		emit()
	}()
	<-done
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

func TestLifecycleFailuresUseExactSafeReasons(t *testing.T) {
	tests := []struct {
		name   string
		event  string
		reason diagnostic.ServiceFailureReason
		value  string
		emit   func(*diagnostic.Recorder, diagnostic.ServiceFailureReason)
	}{
		{
			name:   "startup",
			event:  "service.start.failed",
			reason: diagnostic.ServiceFailureConfigRequiredMissing,
			value:  "config_required_missing",
			emit:   (*diagnostic.Recorder).ServiceStartFailed,
		},
		{
			name:   "verify key",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureConfigVerifyKeyInvalid,
			value:  "config_verify_key_invalid",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "trusted proxy",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureConfigTrustedProxyInvalid,
			value:  "config_trusted_proxy_cidrs_invalid",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "refresh cookie",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureConfigRefreshCookieInvalid,
			value:  "config_refresh_cookie_policy_invalid",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "run identity",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureConfigRunIdentityInvalid,
			value:  "config_run_as_identity_invalid",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "token verifier",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureTokenVerifierInitialization,
			value:  "token_verifier_initialization_failed",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "TLS files",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureInternalTLSFilesUnreadable,
			value:  "internal_tls_files_unreadable",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "TLS material",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureInternalTLSMaterialInvalid,
			value:  "internal_tls_material_invalid",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "privilege drop",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailurePrivilegeDrop,
			value:  "privilege_drop_failed",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "identity client",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureIdentityClientInitialization,
			value:  "identity_client_initialization_failed",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "HTTP listen",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureHTTPListen,
			value:  "http_listen_failed",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "HTTP serve",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureHTTPServe,
			value:  "http_serve_failed",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "HTTP shutdown",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureHTTPShutdown,
			value:  "http_shutdown_failed",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
		{
			name:   "unknown fallback",
			event:  "service.run.failed",
			reason: diagnostic.ServiceFailureReason(255),
			value:  "unknown_failure",
			emit:   (*diagnostic.Recorder).ServiceRunFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder, logs := observedFatalRecorder()
			invokeFatal(t, func() { test.emit(recorder, test.reason) })

			require.Equal(t, 1, logs.Len())
			entry := logs.All()[0]
			assert.Equal(t, zapcore.FatalLevel, entry.Level)
			assert.Equal(t, test.event, entry.Message)
			assert.Equal(t, test.value, entry.ContextMap()["reason_code"])
			assert.Equal(t, []string{"reason_code"}, sortedKeys(entry))
		})
	}
}

func TestRequestIDGenerationFailureUsesFixedSafeSchema(t *testing.T) {
	recorder, logs := observedRecorder()

	recorder.RequestIDGenerationFailed()

	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	assert.Equal(t, zapcore.ErrorLevel, entry.Level)
	assert.Equal(t, "http.request_id.failed", entry.Message)
	assert.Equal(t, "request_id_generation_failed", entry.ContextMap()["error_code"])
	assert.Equal(t, []string{"error_code"}, sortedKeys(entry))
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
