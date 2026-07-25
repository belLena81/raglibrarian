// Package diagnostic records Retrieval workflow events using only safe,
// allowlisted identifiers and fixed reason codes.
package diagnostic

import "go.uber.org/zap"

type Recorder struct{ log *zap.Logger }

func New(log *zap.Logger) *Recorder {
	if log == nil {
		panic("retrieval diagnostic: logger is required")
	}
	return &Recorder{log: log}
}

func (r *Recorder) MetadataReceived(bookID string) {
	r.log.Info("retrieval.metadata.received", zap.String("book_id", bookID))
}

func (r *Recorder) MetadataCompleted(bookID string) {
	r.log.Info("retrieval.metadata.completed", zap.String("book_id", bookID))
}

func (r *Recorder) MetadataRejected(reason string) {
	r.log.Warn("retrieval.metadata.rejected", zap.String("reason_code", reason))
}

func (r *Recorder) ManifestReceived(bookID string) {
	r.log.Info("retrieval.manifest.received", zap.String("book_id", bookID))
}

func (r *Recorder) ManifestCompleted(bookID string) {
	r.log.Info("retrieval.manifest.completed", zap.String("book_id", bookID))
}

func (r *Recorder) ManifestRejected(reason string) {
	r.log.Warn("retrieval.manifest.rejected", zap.String("reason_code", reason))
}

func (r *Recorder) ManifestTerminalFailureRecorded(bookID, reason string) {
	r.log.Warn("retrieval.manifest.terminal_failure.recorded", zap.String("book_id", bookID), zap.String("reason_code", reason))
}

func (r *Recorder) BatchReceived(bookID string) {
	r.log.Info("retrieval.batch.received", zap.String("book_id", bookID))
}

func (r *Recorder) BatchCompleted(bookID string) {
	r.log.Info("retrieval.batch.completed", zap.String("book_id", bookID))
}

func (r *Recorder) BatchFailed(bookID, reason, detail string) {
	fields := []zap.Field{
		zap.String("book_id", bookID),
		zap.String("reason_code", reason),
	}
	if detail != "" {
		fields = append(fields, zap.String("reason_detail", detail))
	}
	r.log.Warn("retrieval.batch.failed", fields...)
}

func (r *Recorder) Rejected(queue, eventType, contentType, reason, detail string) {
	fields := []zap.Field{
		zap.String("queue", queue),
		zap.String("event_type", eventType),
		zap.String("content_type", contentType),
		zap.String("reason_code", reason),
	}
	if detail != "" {
		fields = append(fields, zap.String("reason_detail", detail))
	}
	r.log.Warn("retrieval.batch.rejected", fields...)
}

func (r *Recorder) BatchRejected(reason string) {
	r.log.Warn("retrieval.batch.rejected", zap.String("reason_code", reason))
}

func (r *Recorder) BatchTerminalFailureRecorded(bookID, reason string) {
	r.log.Warn("retrieval.batch.terminal_failure.recorded", zap.String("book_id", bookID), zap.String("reason_code", reason))
}

func (r *Recorder) VectorDeactivateFailed(bookID string) {
	r.log.Warn("retrieval.vector.deactivate_failed", zap.String("book_id", bookID), zap.String("reason_code", "vector_deactivate_failed"))
}

func (r *Recorder) RetryScheduled(queue, reason, detail string) {
	fields := []zap.Field{
		zap.String("operation", queue),
		zap.String("reason_code", reason),
	}
	if detail != "" {
		fields = append(fields, zap.String("reason_detail", detail))
	}
	r.log.Warn("retrieval.retry.scheduled", fields...)
}

func (r *Recorder) RetryScheduledLegacy(queue, reason string) {
	r.log.Warn("retrieval.retry.scheduled", zap.String("operation", queue), zap.String("reason_code", reason))
}

func (r *Recorder) RetryPublishFailed(queue, reason string) {
	r.log.Warn("retrieval.retry.publish_failed", zap.String("operation", queue), zap.String("reason_code", reason))
}

func (r *Recorder) DeliveryReceived(queue, eventType, messageID, bookID string, attempt int64) {
	fields := []zap.Field{
		zap.String("operation", queue),
		zap.String("event_type", eventType),
		zap.String("message_id", messageID),
		zap.Int64("attempt", attempt),
	}
	if bookID != "" {
		fields = append(fields, zap.String("book_id", bookID))
	}
	r.log.Info("retrieval.delivery.received", fields...)
}

func (r *Recorder) DeliverySettled(queue, eventType, messageID, bookID, disposition string) {
	fields := []zap.Field{
		zap.String("operation", queue),
		zap.String("event_type", eventType),
		zap.String("message_id", messageID),
		zap.String("disposition", disposition),
	}
	if bookID != "" {
		fields = append(fields, zap.String("book_id", bookID))
	}
	r.log.Info("retrieval.delivery.settled", fields...)
}

func (r *Recorder) RetryPublished(queue, eventType, messageID, bookID string, attempt int64) {
	fields := []zap.Field{
		zap.String("operation", queue),
		zap.String("event_type", eventType),
		zap.String("message_id", messageID),
		zap.Int64("attempt", attempt),
	}
	if bookID != "" {
		fields = append(fields, zap.String("book_id", bookID))
	}
	r.log.Info("retrieval.retry.published", fields...)
}

func (r *Recorder) OutboxPublished() {
	r.log.Info("retrieval.outbox.published")
}

func (r *Recorder) OutboxDeferred(reason string) {
	r.log.Warn("retrieval.outbox.deferred", zap.String("reason_code", reason))
}

func (r *Recorder) OutboxMarkedPublished() {
	r.log.Info("retrieval.outbox.marked_published")
}

func (r *Recorder) StaleBatchesRecovered(count int64) {
	if count == 0 {
		return
	}
	r.log.Warn("retrieval.stale_batches.recovered", zap.Int64("result_count", count))
}

func (r *Recorder) BrokerSessionReconnecting() {
	r.log.Warn("retrieval.broker_session.reconnecting")
}
