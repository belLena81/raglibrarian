// Package metrics exposes fixed-label Ingestion operational metrics.
package metrics

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
)

type Recorder struct {
	processed          atomic.Uint64
	failed             atomic.Uint64
	deferred           atomic.Uint64
	postgresReady      atomic.Uint64
	objectStorageReady atomic.Uint64
	rabbitReady        atomic.Uint64
	phaseCounts        [4][9]atomic.Uint64
	phaseSumsNS        [4]atomic.Uint64
}

func (r *Recorder) Processed() { r.processed.Add(1) }
func (r *Recorder) Failed()    { r.failed.Add(1) }
func (r *Recorder) Deferred()  { r.deferred.Add(1) }
func (r *Recorder) SetReadiness(postgres, objectStorage, rabbit bool) {
	set(&r.postgresReady, postgres)
	set(&r.objectStorageReady, objectStorage)
	set(&r.rabbitReady, rabbit)
}

func (r *Recorder) Handler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch request.URL.Path {
		case "/healthz":
			response.Header().Set("Cache-Control", "no-store")
			response.WriteHeader(http.StatusOK)
			return
		case "/readyz":
			response.Header().Set("Cache-Control", "no-store")
			if r.postgresReady.Load() != 1 || r.objectStorageReady.Load() != 1 || r.rabbitReady.Load() != 1 {
				response.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			response.WriteHeader(http.StatusOK)
			return
		case "/metrics":
		default:
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		_, _ = fmt.Fprintf(response, format, r.processed.Load(), r.failed.Load(), r.deferred.Load(), r.postgresReady.Load(), r.objectStorageReady.Load(), r.rabbitReady.Load())
		_, _ = response.Write([]byte(r.phaseMetrics()))
	})
}

var phaseBuckets = [...]time.Duration{100 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, 2 * time.Minute}
var phaseNames = [...]string{"download", "extract_chunk", "artifact_finalize", "total"}

func (r *Recorder) ObservePhase(phase application.ProcessingPhase, duration time.Duration) {
	index := int(phase)
	if index < 0 || index >= len(phaseNames) || duration < 0 {
		return
	}
	for bucket, upper := range phaseBuckets {
		if duration <= upper {
			r.phaseCounts[index][bucket].Add(1)
		}
	}
	r.phaseCounts[index][len(phaseBuckets)].Add(1)
	r.phaseSumsNS[index].Add(uint64(duration)) // #nosec G115 -- negative durations rejected above.
}

func (r *Recorder) phaseMetrics() string {
	var output strings.Builder
	output.WriteString("# TYPE ingestion_processing_phase_duration_seconds histogram\n")
	for phase, name := range phaseNames {
		for bucket, upper := range phaseBuckets {
			fmt.Fprintf(&output, "ingestion_processing_phase_duration_seconds_bucket{phase=%q,le=%q} %d\n", name, strconv.FormatFloat(upper.Seconds(), 'f', -1, 64), r.phaseCounts[phase][bucket].Load())
		}
		fmt.Fprintf(&output, "ingestion_processing_phase_duration_seconds_bucket{phase=%q,le=\"+Inf\"} %d\n", name, r.phaseCounts[phase][len(phaseBuckets)].Load())
		fmt.Fprintf(&output, "ingestion_processing_phase_duration_seconds_sum{phase=%q} %s\n", name, strconv.FormatFloat(float64(r.phaseSumsNS[phase].Load())/float64(time.Second), 'f', 6, 64))
		fmt.Fprintf(&output, "ingestion_processing_phase_duration_seconds_count{phase=%q} %d\n", name, r.phaseCounts[phase][len(phaseBuckets)].Load())
	}
	return output.String()
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
# TYPE ingestion_object_storage_ready gauge
ingestion_object_storage_ready %d
# TYPE ingestion_rabbit_ready gauge
ingestion_rabbit_ready %d
`
