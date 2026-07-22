// Package metrics exposes private fixed-label Answer metrics and health endpoints.
package metrics

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
)

type Recorder struct {
	answered          atomic.Uint64
	emptyEvidence     atomic.Uint64
	providerFailure   atomic.Uint64
	invalidOutput     atomic.Uint64
	capacityExhausted atomic.Uint64
	providerInFlight  atomic.Int64
	retrievalReady    atomic.Uint64
	durationNS        atomic.Uint64
}

func (r *Recorder) Observe(outcome application.Outcome, duration time.Duration) {
	switch outcome {
	case application.OutcomeAnswered:
		r.answered.Add(1)
	case application.OutcomeEmptyEvidence:
		r.emptyEvidence.Add(1)
	case application.OutcomeProviderFailure:
		r.providerFailure.Add(1)
	case application.OutcomeInvalidOutput:
		r.invalidOutput.Add(1)
	case application.OutcomeCapacityExhausted:
		r.capacityExhausted.Add(1)
	}
	if duration >= 0 {
		r.durationNS.Add(uint64(duration)) // #nosec G115 -- negative durations are rejected.
	}
}

func (r *Recorder) ProviderStarted()  { r.providerInFlight.Add(1) }
func (r *Recorder) ProviderFinished() { r.providerInFlight.Add(-1) }
func (r *Recorder) SetRetrievalReady(ready bool) {
	if ready {
		r.retrievalReady.Store(1)
	} else {
		r.retrievalReady.Store(0)
	}
}

func (r *Recorder) Handler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch request.URL.Path {
		case "/healthz":
			response.WriteHeader(http.StatusOK)
		case "/readyz":
			if r.retrievalReady.Load() != 1 {
				response.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			response.WriteHeader(http.StatusOK)
		case "/metrics":
			response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			_, _ = fmt.Fprintf(response, metricFormat, r.answered.Load(), r.emptyEvidence.Load(), r.providerFailure.Load(), r.invalidOutput.Load(),
				r.capacityExhausted.Load(), r.providerInFlight.Load(), r.retrievalReady.Load(), float64(r.durationNS.Load())/float64(time.Second))
		default:
			http.NotFound(response, request)
		}
	})
}

const metricFormat = `# TYPE answer_requests_total counter
answer_requests_total{outcome="answered"} %d
answer_requests_total{outcome="empty_evidence"} %d
answer_requests_total{outcome="provider_failure"} %d
answer_requests_total{outcome="invalid_output"} %d
answer_requests_total{outcome="capacity_exhausted"} %d
# TYPE answer_provider_in_flight gauge
answer_provider_in_flight %d
# TYPE answer_retrieval_ready gauge
answer_retrieval_ready %d
# TYPE answer_request_duration_seconds_sum counter
answer_request_duration_seconds_sum %.6f
`
