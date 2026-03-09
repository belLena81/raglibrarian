package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	qmiddleware "github.com/belLena81/raglibrarian/services/query/middleware"
)

// newObservedLogger returns a zap logger that stores log entries in memory so
// tests can make assertions on what was logged without touching stdout.
func newObservedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return zap.New(core), logs
}

// wrapWithRequestID simulates chi's RequestID middleware so the request_id
// field is populated in the log output under test.
func wrapWithRequestID(next http.Handler) http.Handler {
	return chimiddleware.RequestID(next)
}

func makeHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("body"))
	}
}

func TestRequestLogger_LogsOneLinePerRequest(t *testing.T) {
	log, logs := newObservedLogger()
	mw := qmiddleware.RequestLogger(log)
	handler := wrapWithRequestID(mw(makeHandler(http.StatusOK)))

	req := httptest.NewRequest(http.MethodPost, "/query/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, 1, logs.Len())
}

func TestRequestLogger_2xx_LoggedAtInfoLevel(t *testing.T) {
	log, logs := newObservedLogger()
	mw := qmiddleware.RequestLogger(log)
	handler := wrapWithRequestID(mw(makeHandler(http.StatusOK)))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	httptest.NewRecorder()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require := assert.New(t)
	require.Equal(1, logs.Len())
	assert.Equal(t, zapcore.InfoLevel, logs.All()[0].Level)
}

func TestRequestLogger_4xx_LoggedAtWarnLevel(t *testing.T) {
	log, logs := newObservedLogger()
	mw := qmiddleware.RequestLogger(log)
	handler := wrapWithRequestID(mw(makeHandler(http.StatusBadRequest)))

	req := httptest.NewRequest(http.MethodPost, "/query/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, zapcore.WarnLevel, logs.All()[0].Level)
}

func TestRequestLogger_5xx_LoggedAtErrorLevel(t *testing.T) {
	log, logs := newObservedLogger()
	mw := qmiddleware.RequestLogger(log)
	handler := wrapWithRequestID(mw(makeHandler(http.StatusInternalServerError)))

	req := httptest.NewRequest(http.MethodPost, "/query/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, zapcore.ErrorLevel, logs.All()[0].Level)
}

func TestRequestLogger_ContainsMandatoryFields(t *testing.T) {
	log, logs := newObservedLogger()
	mw := qmiddleware.RequestLogger(log)
	handler := wrapWithRequestID(mw(makeHandler(http.StatusOK)))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	entry := logs.All()[0]
	fieldNames := make(map[string]struct{}, len(entry.Context))
	for _, f := range entry.Context {
		fieldNames[f.Key] = struct{}{}
	}

	for _, required := range []string{"request_id", "method", "path", "status", "latency_ms", "bytes"} {
		assert.Contains(t, fieldNames, required, "missing field: %s", required)
	}
}

func TestRequestLogger_StatusField_MatchesResponse(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"200 OK", http.StatusOK},
		{"201 Created", http.StatusCreated},
		{"400 Bad Request", http.StatusBadRequest},
		{"404 Not Found", http.StatusNotFound},
		{"422 Unprocessable", http.StatusUnprocessableEntity},
		{"500 Internal Error", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, logs := newObservedLogger()
			mw := qmiddleware.RequestLogger(log)
			handler := wrapWithRequestID(mw(makeHandler(tt.status)))

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			entry := logs.All()[0]
			for _, f := range entry.Context {
				if f.Key == "status" {
					assert.Equal(t, int64(tt.status), f.Integer)
				}
			}
		})
	}
}

func TestRequestLogger_MethodAndPath_AreRecorded(t *testing.T) {
	log, logs := newObservedLogger()
	mw := qmiddleware.RequestLogger(log)
	handler := wrapWithRequestID(mw(makeHandler(http.StatusOK)))

	req := httptest.NewRequest(http.MethodPost, "/query/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	entry := logs.All()[0]
	fields := make(map[string]string)
	for _, f := range entry.Context {
		if f.Type == zapcore.StringType {
			fields[f.Key] = f.String
		}
	}

	assert.Equal(t, "POST", fields["method"])
	assert.Equal(t, "/query/", fields["path"])
}
