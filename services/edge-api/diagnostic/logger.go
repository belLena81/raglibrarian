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

// ServiceFailureReason is a closed set of safe lifecycle failure classes.
type ServiceFailureReason uint8

const (
	// ServiceFailureUnknown identifies an unmapped lifecycle failure.
	ServiceFailureUnknown ServiceFailureReason = iota + 1
	// ServiceFailureConfigRequiredMissing identifies missing required configuration.
	ServiceFailureConfigRequiredMissing
	// ServiceFailureConfigVerifyKeyInvalid identifies invalid verification-key configuration.
	ServiceFailureConfigVerifyKeyInvalid
	// ServiceFailureConfigTrustedProxyInvalid identifies invalid trusted-proxy configuration.
	ServiceFailureConfigTrustedProxyInvalid
	// ServiceFailureConfigRefreshCookieInvalid identifies invalid refresh-cookie configuration.
	ServiceFailureConfigRefreshCookieInvalid
	// ServiceFailureConfigRunIdentityInvalid identifies invalid runtime identity configuration.
	ServiceFailureConfigRunIdentityInvalid
	// ServiceFailureTokenVerifierInitialization identifies verifier construction failure.
	ServiceFailureTokenVerifierInitialization
	// ServiceFailureInternalTLSFilesUnreadable identifies inaccessible internal TLS files.
	ServiceFailureInternalTLSFilesUnreadable
	// ServiceFailureInternalTLSMaterialInvalid identifies malformed internal TLS material.
	ServiceFailureInternalTLSMaterialInvalid
	// ServiceFailurePrivilegeDrop identifies privilege reduction failure.
	ServiceFailurePrivilegeDrop
	// ServiceFailureIdentityClientInitialization identifies Identity client construction failure.
	ServiceFailureIdentityClientInitialization
	// ServiceFailureRetrievalClientInitialization identifies Retrieval client construction failure.
	ServiceFailureRetrievalClientInitialization
	// ServiceFailureHTTPListen identifies HTTP listener creation failure.
	ServiceFailureHTTPListen
	// ServiceFailureHTTPServe identifies HTTP serving failure after listener creation.
	ServiceFailureHTTPServe
	// ServiceFailureHTTPShutdown identifies graceful HTTP shutdown failure.
	ServiceFailureHTTPShutdown
)

// AuthFailure is a closed set of safe authentication outcomes.
type AuthFailure uint8

const (
	// AuthInvalidRegistration identifies rejected registration input.
	AuthInvalidRegistration AuthFailure = iota + 1
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

// ServiceStartFailed records a classified startup failure and terminates the process.
func (r *Recorder) ServiceStartFailed(reason ServiceFailureReason) {
	r.log.Fatal("service.start.failed", zap.String("reason_code", serviceFailureReasonValue(reason)))
}

// ServiceRunFailed records a classified runtime failure and terminates the process.
func (r *Recorder) ServiceRunFailed(reason ServiceFailureReason) {
	r.log.Fatal("service.run.failed", zap.String("reason_code", serviceFailureReasonValue(reason)))
}

// RequestIDGenerationFailed records an entropy failure without request data.
func (r *Recorder) RequestIDGenerationFailed() {
	r.log.Error("http.request_id.failed", zap.String("error_code", "request_id_generation_failed"))
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

func serviceFailureReasonValue(reason ServiceFailureReason) string {
	switch reason {
	case ServiceFailureConfigRequiredMissing:
		return "config_required_missing"
	case ServiceFailureConfigVerifyKeyInvalid:
		return "config_verify_key_invalid"
	case ServiceFailureConfigTrustedProxyInvalid:
		return "config_trusted_proxy_cidrs_invalid"
	case ServiceFailureConfigRefreshCookieInvalid:
		return "config_refresh_cookie_policy_invalid"
	case ServiceFailureConfigRunIdentityInvalid:
		return "config_run_as_identity_invalid"
	case ServiceFailureTokenVerifierInitialization:
		return "token_verifier_initialization_failed"
	case ServiceFailureInternalTLSFilesUnreadable:
		return "internal_tls_files_unreadable"
	case ServiceFailureInternalTLSMaterialInvalid:
		return "internal_tls_material_invalid"
	case ServiceFailurePrivilegeDrop:
		return "privilege_drop_failed"
	case ServiceFailureIdentityClientInitialization:
		return "identity_client_initialization_failed"
	case ServiceFailureRetrievalClientInitialization:
		return "retrieval_client_initialization_failed"
	case ServiceFailureHTTPListen:
		return "http_listen_failed"
	case ServiceFailureHTTPServe:
		return "http_serve_failed"
	case ServiceFailureHTTPShutdown:
		return "http_shutdown_failed"
	default:
		return "unknown_failure"
	}
}

func registrationFailureValue(failure AuthFailure) (string, bool) {
	switch failure {
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
