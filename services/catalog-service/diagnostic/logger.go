// Package diagnostic records fixed Catalog operational outcomes without event
// payloads, object keys, checksums, or broker error strings.
package diagnostic

import "go.uber.org/zap"

type Recorder struct{ log *zap.Logger }

func New(log *zap.Logger) *Recorder {
	if log == nil {
		panic("catalog diagnostic: logger is required")
	}
	return &Recorder{log: log}
}

func (r *Recorder) OutboxClaimFailed() { r.log.Warn("catalog.outbox.claim.failed") }
func (r *Recorder) OutboxRetryFailed() { r.log.Warn("catalog.outbox.retry.failed") }
func (r *Recorder) OutboxMarkFailed()  { r.log.Warn("catalog.outbox.mark.failed") }
