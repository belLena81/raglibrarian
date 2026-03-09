// Package middleware contains HTTP middleware for the query service.
// Logging middleware lives here — not in the handler — so handlers stay
// focused on request/response logic and remain free of logging concerns.
package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// RequestLogger returns a chi-compatible middleware that:
//   - Echoes the request ID from context into the X-Request-Id response header,
//     so clients can correlate their requests with server log lines.
//   - Emits one structured zap log line per request after the response is written.
//
// Every log line carries:
//
//	request_id  — from chi's RequestID middleware (must be applied first)
//	method      — HTTP verb
//	path        — raw request URI
//	status      — HTTP response status code
//	latency_ms  — wall-clock duration in milliseconds
//	bytes       — response body size in bytes
//
// 5xx responses are logged at Error level; 4xx at Warn; everything else at Info.
func RequestLogger(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Echo the request ID into the response so clients can correlate
			// their request with server log lines without parsing log output.
			// Must be set before the handler writes the status line.
			if reqID := middleware.GetReqID(r.Context()); reqID != "" {
				w.Header().Set("X-Request-Id", reqID)
			}

			// chi's WrapResponseWriter captures status code and bytes written
			// without buffering the body.
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			latency := time.Since(start)
			status := ww.Status()

			fields := []zapcore.Field{
				zap.String("request_id", middleware.GetReqID(r.Context())),
				zap.String("method", r.Method),
				zap.String("path", r.RequestURI),
				zap.Int("status", status),
				zap.Int64("latency_ms", latency.Milliseconds()),
				zap.Int("bytes", ww.BytesWritten()),
			}

			switch {
			case status >= http.StatusInternalServerError:
				log.Error("request completed", fields...)
			case status >= http.StatusBadRequest:
				log.Warn("request completed", fields...)
			default:
				log.Info("request completed", fields...)
			}
		})
	}
}
