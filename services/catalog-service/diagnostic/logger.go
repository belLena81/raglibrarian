// Package diagnostic records Catalog operation outcomes using only safe,
// allowlisted identifiers and aggregate metadata.
package diagnostic

import (
	"encoding/hex"

	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
)

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

// UploadCompleted records the durable identity and aggregate properties of an
// uploaded book. It never records names, emails, titles, authors, object keys,
// document bytes, or event payloads.
func (r *Recorder) UploadCompleted(requestID string, actor catalog.Actor, book catalog.Book) {
	r.log.Info("catalog upload completed",
		zap.String("request_id", requestID),
		zap.String("actor_id", actor.UserID),
		zap.String("role", actor.Role),
		zap.String("account_status", actor.Status),
		zap.String("book_id", book.ID),
		zap.Int64("byte_size", book.ByteSize),
		zap.Int("tag_count", len(book.Metadata.Tags)),
		zap.String("checksum_sha256", hex.EncodeToString(book.Checksum[:])),
	)
}

// OperationRejected records a safe failure class with the calling actor.
func (r *Recorder) OperationRejected(operation, requestID string, actor catalog.Actor, reason string) {
	r.log.Warn("catalog operation rejected",
		zap.String("operation", operation),
		zap.String("request_id", requestID),
		zap.String("actor_id", actor.UserID),
		zap.String("role", actor.Role),
		zap.String("account_status", actor.Status),
		zap.String("reason_code", reason),
	)
}

// ListCompleted records pagination and result counts without book metadata.
func (r *Recorder) ListCompleted(requestID string, actor catalog.Actor, pageSize, resultCount int) {
	r.log.Info("catalog list completed",
		zap.String("request_id", requestID),
		zap.String("actor_id", actor.UserID),
		zap.String("role", actor.Role),
		zap.String("account_status", actor.Status),
		zap.Int("page_size", pageSize),
		zap.Int("result_count", resultCount),
	)
}

// GetCompleted records the requested book identifier and calling actor.
func (r *Recorder) GetCompleted(requestID string, actor catalog.Actor, bookID string) {
	r.log.Info("catalog get completed",
		zap.String("request_id", requestID),
		zap.String("actor_id", actor.UserID),
		zap.String("role", actor.Role),
		zap.String("account_status", actor.Status),
		zap.String("book_id", bookID),
	)
}
