// Package metrics exposes fixed-label Ingestion operational metrics.
package metrics

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

type Recorder struct {
	processed     atomic.Uint64
	failed        atomic.Uint64
	deferred      atomic.Uint64
	postgresReady atomic.Uint64
	minioReady    atomic.Uint64
	rabbitReady   atomic.Uint64
}

func (r *Recorder) Processed() { r.processed.Add(1) }
func (r *Recorder) Failed()    { r.failed.Add(1) }
func (r *Recorder) Deferred()  { r.deferred.Add(1) }
func (r *Recorder) SetReadiness(postgres, minio, rabbit bool) {
	set(&r.postgresReady, postgres)
	set(&r.minioReady, minio)
	set(&r.rabbitReady, rabbit)
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
		_, _ = fmt.Fprintf(response, format, r.processed.Load(), r.failed.Load(), r.deferred.Load(), r.postgresReady.Load(), r.minioReady.Load(), r.rabbitReady.Load())
	})
}

func set(target *atomic.Uint64, value bool) {
	if value {
		target.Store(1)
	} else {
		target.Store(0)
	}
}

const format = `# TYPE ingestion_processed_total counter
ingestion_processed_total %d
# TYPE ingestion_failed_total counter
ingestion_failed_total %d
# TYPE ingestion_deferred_total counter
ingestion_deferred_total %d
# TYPE ingestion_postgres_ready gauge
ingestion_postgres_ready %d
# TYPE ingestion_minio_ready gauge
ingestion_minio_ready %d
# TYPE ingestion_rabbit_ready gauge
ingestion_rabbit_ready %d
`
