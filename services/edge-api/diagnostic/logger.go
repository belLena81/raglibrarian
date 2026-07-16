// Package diagnostic is the only Edge package that constructs structured log
// events. Its event-specific API prevents callers from attaching arbitrary
// fields, errors, or submitted values.
package diagnostic

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

// RequestOutcome is a closed set of HTTP completion outcomes.
type RequestOutcome uint8

const (
	// RequestSuccess identifies a successfully handled request.
	RequestSuccess RequestOutcome = iota + 1
	// RequestClientError identifies a rejected client request.
	RequestClientError
	// RequestServerError identifies an internal failure.
	RequestServerError
	// RequestNotImplemented identifies a truthful unavailable capability.
	RequestNotImplemented
	// RequestResponseAborted identifies a panic after response commitment.
	RequestResponseAborted
)

// AuthFailure is a closed set of safe authentication outcomes.
type AuthFailure uint8

const (
	// AuthEmailConflict identifies an existing registration without exposing an email.
	AuthEmailConflict AuthFailure = iota + 1
	// AuthInvalidRegistration identifies rejected registration input.
	AuthInvalidRegistration
	// AuthInvalidCredentials identifies a rejected login.
	AuthInvalidCredentials
	// AuthDependencyUnavailable identifies an unavailable Identity dependency.
	AuthDependencyUnavailable
)

// Recorder emits only fixed, allowlisted Edge diagnostic events.
type Recorder struct {
	log *zap.Logger
}

// New constructs an allowlisted diagnostic recorder.
func New(log *zap.Logger) *Recorder {
	if log == nil {
		panic("diagnostic: logger is required")
	}
	return &Recorder{log: log}
}

// ServiceStartFailed records a fixed startup failure and terminates the process.
func (r *Recorder) ServiceStartFailed() {
	r.log.Fatal("service.start.failed")
}

// ServiceRunFailed records a fixed runtime failure and terminates the process.
func (r *Recorder) ServiceRunFailed() {
	r.log.Fatal("service.run.failed")
}

// RequestCompleted records one HTTP completion event.
func (r *Recorder) RequestCompleted(request *http.Request, status int, outcome RequestOutcome, duration time.Duration, responseBytes int) {
	requestID, ok := requestFields(request)
	if !ok {
		return
	}
	outcomeValue, ok := requestOutcomeValue(outcome)
	if !ok {
		return
	}
	if duration < 0 {
		duration = 0
	}
	if responseBytes < 0 {
		responseBytes = 0
	}
	switch outcome {
	case RequestServerError, RequestResponseAborted:
		r.log.Error("http.request.completed",
			zap.String("request_id", requestID),
			zap.String("method", normalizedMethod(request.Method)),
			zap.String("route", routeTemplate(request)),
			zap.Int("status", status),
			zap.String("outcome", outcomeValue),
			zap.Int64("duration_ms", duration.Milliseconds()),
			zap.Int("response_bytes", responseBytes),
		)
	case RequestClientError, RequestNotImplemented:
		r.log.Warn("http.request.completed",
			zap.String("request_id", requestID),
			zap.String("method", normalizedMethod(request.Method)),
			zap.String("route", routeTemplate(request)),
			zap.Int("status", status),
			zap.String("outcome", outcomeValue),
			zap.Int64("duration_ms", duration.Milliseconds()),
			zap.Int("response_bytes", responseBytes),
		)
	default:
		r.log.Info("http.request.completed",
			zap.String("request_id", requestID),
			zap.String("method", normalizedMethod(request.Method)),
			zap.String("route", routeTemplate(request)),
			zap.Int("status", status),
			zap.String("outcome", outcomeValue),
			zap.Int64("duration_ms", duration.Milliseconds()),
			zap.Int("response_bytes", responseBytes),
		)
	}
}

// PanicRecovered records a fixed panic event without the panic value or stack.
func (r *Recorder) PanicRecovered(request *http.Request) {
	requestID, ok := requestFields(request)
	if !ok {
		return
	}
	r.log.Error("http.panic.recovered",
		zap.String("request_id", requestID),
		zap.String("method", normalizedMethod(request.Method)),
		zap.String("route", routeTemplate(request)),
		zap.String("error_code", "internal_panic"),
		zap.String("stack_fingerprint", stackFingerprint()),
	)
}

// TokenRejected records a token-validation rejection without token details.
func (r *Recorder) TokenRejected(request *http.Request) {
	requestID, ok := requestFields(request)
	if !ok {
		return
	}
	r.log.Debug("auth.token.rejected",
		zap.String("request_id", requestID),
		zap.String("outcome", "invalid_token"),
	)
}

// RegistrationFailed records a safe registration outcome.
func (r *Recorder) RegistrationFailed(request *http.Request, failure AuthFailure) {
	value, ok := registrationFailureValue(failure)
	if !ok {
		return
	}
	requestID, ok := requestFields(request)
	if !ok {
		return
	}
	r.log.Debug("auth.register.failed",
		zap.String("request_id", requestID),
		zap.String("outcome", value),
	)
}

// LoginFailed records a safe login outcome.
func (r *Recorder) LoginFailed(request *http.Request, failure AuthFailure) {
	value, ok := loginFailureValue(failure)
	if !ok {
		return
	}
	requestID, ok := requestFields(request)
	if !ok {
		return
	}
	r.log.Debug("auth.login.failed",
		zap.String("request_id", requestID),
		zap.String("outcome", value),
	)
}

// RetrievalUnavailable records the truthful unavailable query capability.
func (r *Recorder) RetrievalUnavailable(request *http.Request) {
	requestID, ok := requestFields(request)
	if !ok {
		return
	}
	r.log.Debug("query.retrieval.unavailable",
		zap.String("request_id", requestID),
		zap.String("outcome", "not_implemented"),
	)
}

func requestFields(request *http.Request) (string, bool) {
	if request == nil {
		return "", false
	}
	requestID := chimiddleware.GetReqID(request.Context())
	if len(requestID) != 32 || strings.ToLower(requestID) != requestID {
		return "", false
	}
	decoded, err := hex.DecodeString(requestID)
	if err != nil || len(decoded) != 16 {
		return "", false
	}
	return requestID, true
}

func normalizedMethod(method string) string {
	switch method {
	case http.MethodConnect,
		http.MethodDelete,
		http.MethodGet,
		http.MethodHead,
		http.MethodOptions,
		http.MethodPatch,
		http.MethodPost,
		http.MethodPut,
		http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

func routeTemplate(request *http.Request) string {
	routeContext := chi.RouteContext(request.Context())
	if routeContext == nil {
		return "unmatched"
	}
	pattern := strings.TrimSpace(routeContext.RoutePattern())
	if pattern == "" {
		return "unmatched"
	}
	return pattern
}

func requestOutcomeValue(outcome RequestOutcome) (string, bool) {
	switch outcome {
	case RequestSuccess:
		return "success", true
	case RequestClientError:
		return "client_error", true
	case RequestServerError:
		return "server_error", true
	case RequestNotImplemented:
		return "not_implemented", true
	case RequestResponseAborted:
		return "response_aborted", true
	default:
		return "", false
	}
}

func registrationFailureValue(failure AuthFailure) (string, bool) {
	switch failure {
	case AuthEmailConflict:
		return "email_conflict", true
	case AuthInvalidRegistration:
		return "invalid_registration", true
	case AuthDependencyUnavailable:
		return "dependency_unavailable", true
	default:
		return "", false
	}
}

func loginFailureValue(failure AuthFailure) (string, bool) {
	switch failure {
	case AuthInvalidCredentials:
		return "invalid_credentials", true
	case AuthDependencyUnavailable:
		return "dependency_unavailable", true
	default:
		return "", false
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
