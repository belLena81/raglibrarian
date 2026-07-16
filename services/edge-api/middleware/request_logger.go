// Package middleware contains HTTP middleware for the query service.
package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// RequestLogger emits one allowlisted structured completion event per request.
func RequestLogger(log *zap.Logger) func(http.Handler) http.Handler {
	if log == nil {
		panic("middleware: request logger is required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				if recovered := recover(); recovered != nil {
					status := ww.Status()
					outcome := "response_aborted"
					if status == 0 {
						status = http.StatusInternalServerError
						outcome = "server_error"
					}
					logRequestCompletion(log, r, ww, start, status, outcome)
					panic(recovered)
				}
				status := ww.Status()
				if status == 0 {
					status = http.StatusOK
				}
				logRequestCompletion(log, r, ww, start, status, requestOutcome(status))
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

func logRequestCompletion(
	log *zap.Logger,
	r *http.Request,
	ww middleware.WrapResponseWriter,
	start time.Time,
	status int,
	outcome string,
) {
	fields := []zapcore.Field{
		zap.String("request_id", middleware.GetReqID(r.Context())),
		zap.String("method", normalizedMethod(r.Method)),
		zap.String("route", routeTemplate(r)),
		zap.Int("status", status),
		zap.String("outcome", outcome),
		zap.Int64("duration_ms", time.Since(start).Milliseconds()),
		zap.Int("response_bytes", ww.BytesWritten()),
	}

	switch {
	case outcome == "server_error" || outcome == "response_aborted":
		log.Error("http.request.completed", fields...)
	case status >= http.StatusBadRequest:
		log.Warn("http.request.completed", fields...)
	default:
		log.Info("http.request.completed", fields...)
	}
}

func requestOutcome(status int) string {
	switch {
	case status == http.StatusNotImplemented:
		return "not_implemented"
	case status >= http.StatusInternalServerError:
		return "server_error"
	case status >= http.StatusBadRequest:
		return "client_error"
	default:
		return "success"
	}
}
