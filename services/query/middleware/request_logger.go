// Package middleware contains HTTP middleware for the query service.
package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// RequestLogger emits one structured log line per request and echoes the
// request ID into the X-Request-Id response header.
// Logs 5xx at Error, 4xx at Warn, everything else at Info.
func RequestLogger(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			if reqID := middleware.GetReqID(r.Context()); reqID != "" {
				w.Header().Set("X-Request-Id", reqID)
			}

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
