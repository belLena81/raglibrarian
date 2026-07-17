package metrics

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/belLena81/raglibrarian/services/catalog-service/diagnostic"
)

type Recorder struct {
	diagnostics *diagnostic.Recorder

	outboxClaimFailures   atomic.Uint64
	outboxPublishFailures atomic.Uint64
	outboxRetryFailures   atomic.Uint64
	outboxMarkFailures    atomic.Uint64
	reconcileScanned      atomic.Uint64
	reconcileDeleted      atomic.Uint64
	reconcileFailures     atomic.Uint64
	postgresReady         atomic.Uint64
	minioReady            atomic.Uint64
	outboxPending         atomic.Int64
	outboxOldestAge       atomic.Int64
}

func New(diagnostics *diagnostic.Recorder) *Recorder {
	return &Recorder{diagnostics: diagnostics}
}

func (r *Recorder) OutboxClaimFailed() {
	r.outboxClaimFailures.Add(1)
	if r.diagnostics != nil {
		r.diagnostics.OutboxClaimFailed()
	}
}

func (r *Recorder) OutboxPublishFailed() { r.outboxPublishFailures.Add(1) }

func (r *Recorder) OutboxRetryFailed() {
	r.outboxRetryFailures.Add(1)
	if r.diagnostics != nil {
		r.diagnostics.OutboxRetryFailed()
	}
}

func (r *Recorder) OutboxMarkFailed() {
	r.outboxMarkFailures.Add(1)
	if r.diagnostics != nil {
		r.diagnostics.OutboxMarkFailed()
	}
}

func (r *Recorder) ReconciliationCompleted(scanned, deleted int) {
	if scanned > 0 {
		r.reconcileScanned.Add(uint64(scanned))
	}
	if deleted > 0 {
		r.reconcileDeleted.Add(uint64(deleted))
	}
}

func (r *Recorder) ReconciliationFailed() { r.reconcileFailures.Add(1) }

func (r *Recorder) SetReadiness(postgres, minio bool) {
	setBool(&r.postgresReady, postgres)
	setBool(&r.minioReady, minio)
}

func (r *Recorder) SetOutboxBacklog(pending, oldestAgeSeconds int64) {
	r.outboxPending.Store(max(pending, 0))
	r.outboxOldestAge.Store(max(oldestAgeSeconds, 0))
}

func (r *Recorder) Handler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/metrics" {
			http.NotFound(response, request)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		_, _ = fmt.Fprintf(response, metricsFormat,
			r.outboxClaimFailures.Load(), r.outboxPublishFailures.Load(), r.outboxRetryFailures.Load(), r.outboxMarkFailures.Load(),
			r.reconcileScanned.Load(), r.reconcileDeleted.Load(), r.reconcileFailures.Load(),
			r.postgresReady.Load(), r.minioReady.Load(), r.outboxPending.Load(), r.outboxOldestAge.Load())
	})
}

func setBool(value *atomic.Uint64, enabled bool) {
	if enabled {
		value.Store(1)
		return
	}
	value.Store(0)
}

const metricsFormat = `# TYPE catalog_outbox_claim_failures_total counter
catalog_outbox_claim_failures_total %d
# TYPE catalog_outbox_publish_failures_total counter
catalog_outbox_publish_failures_total %d
# TYPE catalog_outbox_retry_failures_total counter
catalog_outbox_retry_failures_total %d
# TYPE catalog_outbox_mark_failures_total counter
catalog_outbox_mark_failures_total %d
# TYPE catalog_reconciliation_scanned_total counter
catalog_reconciliation_scanned_total %d
# TYPE catalog_reconciliation_deleted_total counter
catalog_reconciliation_deleted_total %d
# TYPE catalog_reconciliation_failures_total counter
catalog_reconciliation_failures_total %d
# TYPE catalog_postgres_ready gauge
catalog_postgres_ready %d
# TYPE catalog_minio_ready gauge
catalog_minio_ready %d
# TYPE catalog_outbox_pending gauge
catalog_outbox_pending %d
# TYPE catalog_outbox_oldest_age_seconds gauge
catalog_outbox_oldest_age_seconds %d
`
