// Package diagnostic is the only Identity package that emits application logs.
package diagnostic

import (
	"go.uber.org/zap"
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
