package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"runtime"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type recoveredErrorResponse struct {
	Code      string `json:"code"`
	Error     string `json:"error"`
	RequestID string `json:"request_id"`
}

// Recovery converts panics into a stable error without exposing the panic
// value or stack. The logged fingerprint is derived from source frames only.
func Recovery(log *zap.Logger) func(http.Handler) http.Handler {
	if log == nil {
		panic("middleware: recovery logger is required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				if recover() == nil {
					return
				}

				requestID := chimiddleware.GetReqID(r.Context())
				fields := []zapcore.Field{
					zap.String("request_id", requestID),
					zap.String("method", normalizedMethod(r.Method)),
					zap.String("route", routeTemplate(r)),
					zap.String("error_code", "internal_panic"),
					zap.String("stack_fingerprint", stackFingerprint()),
				}
				log.Error("http.panic.recovered", fields...)

				if ww.Status() != 0 {
					return
				}
				ww.Header().Set("Content-Type", "application/json")
				ww.Header().Set("Cache-Control", "no-store, private")
				ww.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(ww).Encode(recoveredErrorResponse{
					Code:      "internal_error",
					Error:     "internal server error",
					RequestID: requestID,
				})
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

func stackFingerprint() string {
	programCounters := make([]uintptr, 32)
	count := runtime.Callers(3, programCounters)
	frames := runtime.CallersFrames(programCounters[:count])
	hash := sha256.New()
	for {
		frame, more := frames.Next()
		_, _ = hash.Write([]byte(frame.Function))
		_, _ = hash.Write([]byte{'\n'})
		_, _ = hash.Write([]byte(filepath.Base(frame.File)))
		_, _ = hash.Write([]byte{'\n'})
		if !more {
			break
		}
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}
