// Package diagnostic is the only Identity package that emits application logs.
package diagnostic

import (
	"context"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// Stage identifies a bounded worker operation without carrying sensitive data.
type Stage string

// Identity worker stages permitted in structured diagnostics.
const (
	StageSessionCleanup      Stage = "session_cleanup"
	StageVerificationCleanup Stage = "verification_cleanup"
	StageRejectedCleanup     Stage = "rejected_cleanup"
	StageEmailClaim          Stage = "email_claim"
	StageEmailMark           Stage = "email_mark"
	StageEmailRetry          Stage = "email_retry"
	StageEmailExhausted      Stage = "email_exhausted"
)

// Recorder emits allowlisted Identity lifecycle and worker diagnostics.
type Recorder struct{ log *zap.Logger }

// New constructs a Recorder around the service logger.
func New(log *zap.Logger) *Recorder {
	if log == nil {
		panic("diagnostic: logger is required")
	}
	return &Recorder{log: log}
}

// UnaryServerInterceptor emits completion diagnostics for allowlisted Identity
// operations. It never records request or response fields.
func (r *Recorder) UnaryServerInterceptor(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	operation, ok := identityOperation(info.FullMethod)
	if !ok {
		return handler(ctx, req)
	}
	started := time.Now()
	response, err := handler(ctx, req)
	r.log.Info("grpc.request.completed",
		zap.String("operation", operation),
		zap.String("code", status.Code(err).String()),
		zap.Int64("duration_ms", time.Since(started).Milliseconds()),
	)
	return response, err
}

// StreamServerInterceptor applies the same no-payload completion record to
// Identity streaming operations. Stream requests and responses are untrusted
// and may contain sensitive data, so neither is inspected or logged.
func (r *Recorder) StreamServerInterceptor(
	srv any,
	stream grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	operation, ok := identityOperation(info.FullMethod)
	if !ok {
		return handler(srv, stream)
	}
	started := time.Now()
	err := handler(srv, stream)
	r.log.Info("grpc.request.completed",
		zap.String("operation", operation),
		zap.String("code", status.Code(err).String()),
		zap.Int64("duration_ms", time.Since(started).Milliseconds()),
	)
	return err
}

// ServiceStartFailed records an unrecoverable startup failure and terminates.
func (r *Recorder) ServiceStartFailed() { r.log.Fatal("service.start.failed") }

// ServiceRunFailed records an unrecoverable runtime failure and terminates.
func (r *Recorder) ServiceRunFailed() { r.log.Fatal("service.run.failed") }

// WorkerFailed records a sanitized failure for a known worker stage.
func (r *Recorder) WorkerFailed(stage Stage) {
	if !stage.valid() {
		return
	}
	r.log.Warn("worker.operation.failed", zap.String("stage", string(stage)))
}

// WorkerCompleted records successful completion of a known worker stage.
func (r *Recorder) WorkerCompleted(stage Stage) {
	if !stage.valid() {
		return
	}
	r.log.Info("worker.operation.completed", zap.String("stage", string(stage)))
}

func (s Stage) valid() bool {
	switch s {
	case StageSessionCleanup, StageVerificationCleanup, StageRejectedCleanup,
		StageEmailClaim, StageEmailMark, StageEmailRetry, StageEmailExhausted:
		return true
	default:
		return false
	}
}

func identityOperation(method string) (string, bool) {
	switch method {
	case "/identity.v1.IdentityService/Register":
		return "register", true
	case "/identity.v1.IdentityService/VerifyEmail":
		return "verify_email", true
	case "/identity.v1.IdentityService/ResendVerification":
		return "resend_verification", true
	case "/identity.v1.IdentityService/RequestPasswordReset":
		return "password_reset_request", true
	case "/identity.v1.IdentityService/VerifyPasswordReset":
		return "password_reset_verify", true
	case "/identity.v1.IdentityService/CompletePasswordReset":
		return "password_reset_complete", true
	case "/identity.v1.IdentityService/Login":
		return "login", true
	case "/identity.v1.IdentityService/Refresh":
		return "refresh", true
	case "/identity.v1.IdentityService/Logout":
		return "logout", true
	case "/identity.v1.IdentityService/ValidateSession":
		return "validate_session", true
	case "/identity.v1.IdentityService/GetSetupStatus":
		return "get_setup_status", true
	case "/identity.v1.IdentityService/BootstrapAdmin":
		return "create_admin", true
	case "/identity.v1.IdentityService/ListPendingLibrarians":
		return "list_pending_librarians", true
	case "/identity.v1.IdentityService/ApproveLibrarian":
		return "approve_librarian", true
	case "/identity.v1.IdentityService/RejectLibrarian":
		return "reject_librarian", true
	case "/identity.v1.IdentityService/WatchPendingLibrarians":
		return "watch_pending_librarians", true
	default:
		return "", false
	}
}
