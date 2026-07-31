// Package metrics exposes private fixed-label Answer metrics and health endpoints.
package metrics

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
)

type Recorder struct {
	cacheOperationalMu       sync.Mutex
	cacheSequence            uint64
	answered                 atomic.Uint64
	emptyEvidence            atomic.Uint64
	retrievalFailure         atomic.Uint64
	providerFailure          atomic.Uint64
	invalidOutput            atomic.Uint64
	capacityExhausted        atomic.Uint64
	cacheBypass              atomic.Uint64
	cacheHit                 atomic.Uint64
	cacheMiss                atomic.Uint64
	cacheStale               atomic.Uint64
	cacheModeMismatch        atomic.Uint64
	cacheTopicMismatch       atomic.Uint64
	cacheSemanticMismatch    atomic.Uint64
	cacheGuardMismatch       atomic.Uint64
	cacheHardMismatch        atomic.Uint64
	cacheEvidenceMismatch    atomic.Uint64
	cacheGenerationCoalesced atomic.Uint64
	cacheValidationMismatch  atomic.Uint64
	cacheEnabled             atomic.Uint64
	cacheCapacity            atomic.Int64
	cacheTTLSeconds          atomic.Int64
	cacheMinimumCosineMillis atomic.Int64
	cacheEntries             atomic.Int64
	cacheStores              atomic.Uint64
	cacheEvictions           atomic.Uint64
	cacheExpired             atomic.Uint64
	providerRequests         atomic.Uint64
	providerInFlight         atomic.Int64
	retrievalReady           atomic.Uint64
	durationNS               atomic.Uint64
}

func (r *Recorder) Observe(outcome application.Outcome, duration time.Duration) {
	switch outcome {
	case application.OutcomeAnswered:
		r.answered.Add(1)
	case application.OutcomeEmptyEvidence:
		r.emptyEvidence.Add(1)
	case application.OutcomeRetrievalFailure:
		r.retrievalFailure.Add(1)
	case application.OutcomeGeneratorFailure:
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

func (r *Recorder) GeneratorStarted() {
	r.providerRequests.Add(1)
	r.providerInFlight.Add(1)
}
func (r *Recorder) GeneratorFinished() { r.providerInFlight.Add(-1) }
func (r *Recorder) CacheLookup(outcome application.CacheOutcome) {
	switch outcome {
	case application.CacheOutcomeBypass:
		r.cacheBypass.Add(1)
	case application.CacheOutcomeHit:
		r.cacheHit.Add(1)
	case application.CacheOutcomeMiss:
		r.cacheMiss.Add(1)
	case application.CacheOutcomeStale:
		r.cacheStale.Add(1)
	case application.CacheOutcomeModeMismatch:
		r.cacheModeMismatch.Add(1)
	case application.CacheOutcomeTopicMismatch:
		r.cacheTopicMismatch.Add(1)
	case application.CacheOutcomeSemanticMismatch:
		r.cacheSemanticMismatch.Add(1)
	case application.CacheOutcomeGuardMismatch:
		r.cacheGuardMismatch.Add(1)
	case application.CacheOutcomeHardMismatch:
		r.cacheHardMismatch.Add(1)
	case application.CacheOutcomeEvidenceMismatch:
		r.cacheEvidenceMismatch.Add(1)
	case application.CacheOutcomeGenerationCoalesced:
		r.cacheGenerationCoalesced.Add(1)
	case application.CacheOutcomeValidationMismatch:
		r.cacheValidationMismatch.Add(1)
	}
}
func (r *Recorder) CacheConfigured(state application.CacheState) {
	if state.Enabled {
		r.cacheEnabled.Store(1)
	} else {
		r.cacheEnabled.Store(0)
	}
	r.cacheCapacity.Store(int64(state.Capacity))
	r.cacheTTLSeconds.Store(state.TTLSeconds)
	r.cacheMinimumCosineMillis.Store(int64(state.MinimumCosineMillis))
}
func (r *Recorder) CacheOperational(state application.CacheOperationalState) {
	r.cacheOperationalMu.Lock()
	defer r.cacheOperationalMu.Unlock()
	if state.Sequence < r.cacheSequence {
		return
	}
	r.cacheSequence = state.Sequence
	r.cacheEntries.Store(int64(state.Entries))
	r.cacheStores.Store(state.Stores)
	r.cacheEvictions.Store(state.Evictions)
	r.cacheExpired.Store(state.Expired)
}
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
			_, _ = fmt.Fprintf(response, metricFormat, r.answered.Load(), r.emptyEvidence.Load(), r.retrievalFailure.Load(), r.providerFailure.Load(), r.invalidOutput.Load(),
				r.capacityExhausted.Load(), r.cacheBypass.Load(), r.cacheHit.Load(), r.cacheMiss.Load(), r.cacheStale.Load(), r.cacheModeMismatch.Load(), r.cacheTopicMismatch.Load(),
				r.cacheSemanticMismatch.Load(), r.cacheGuardMismatch.Load(), r.cacheHardMismatch.Load(), r.cacheEvidenceMismatch.Load(),
				r.cacheGenerationCoalesced.Load(), r.cacheValidationMismatch.Load(), r.cacheEnabled.Load(), r.cacheCapacity.Load(), r.cacheTTLSeconds.Load(),
				r.cacheMinimumCosineMillis.Load(), r.cacheEntries.Load(), r.cacheStores.Load(), r.cacheEvictions.Load(), r.cacheExpired.Load(),
				r.providerRequests.Load(), r.providerInFlight.Load(),
				r.retrievalReady.Load(), float64(r.durationNS.Load())/float64(time.Second))
		default:
			http.NotFound(response, request)
		}
	})
}

const metricFormat = `# TYPE answer_requests_total counter
answer_requests_total{outcome="answered"} %d
answer_requests_total{outcome="empty_evidence"} %d
answer_requests_total{outcome="retrieval_failure"} %d
answer_requests_total{outcome="provider_failure"} %d
answer_requests_total{outcome="invalid_output"} %d
answer_requests_total{outcome="capacity_exhausted"} %d
# TYPE answer_cache_lookups_total counter
answer_cache_lookups_total{outcome="bypass"} %d
answer_cache_lookups_total{outcome="hit"} %d
answer_cache_lookups_total{outcome="miss"} %d
answer_cache_lookups_total{outcome="stale"} %d
answer_cache_lookups_total{outcome="mode_mismatch"} %d
answer_cache_lookups_total{outcome="topic_mismatch"} %d
answer_cache_lookups_total{outcome="semantic_mismatch"} %d
answer_cache_lookups_total{outcome="guard_mismatch"} %d
answer_cache_lookups_total{outcome="hard_mismatch"} %d
answer_cache_lookups_total{outcome="evidence_mismatch"} %d
answer_cache_lookups_total{outcome="generation_coalesced"} %d
answer_cache_lookups_total{outcome="validation_mismatch"} %d
# TYPE answer_cache_enabled gauge
answer_cache_enabled %d
# TYPE answer_cache_capacity gauge
answer_cache_capacity %d
# TYPE answer_cache_ttl_seconds gauge
answer_cache_ttl_seconds %d
# TYPE answer_cache_minimum_cosine_millis gauge
answer_cache_minimum_cosine_millis %d
# TYPE answer_cache_entries gauge
answer_cache_entries %d
# TYPE answer_cache_stores_total counter
answer_cache_stores_total %d
# TYPE answer_cache_evictions_total counter
answer_cache_evictions_total %d
# TYPE answer_cache_expired_entries_total counter
answer_cache_expired_entries_total %d
# TYPE answer_provider_requests_total counter
answer_provider_requests_total %d
# TYPE answer_provider_in_flight gauge
answer_provider_in_flight %d
# TYPE answer_retrieval_ready gauge
answer_retrieval_ready %d
# TYPE answer_request_duration_seconds_sum counter
answer_request_duration_seconds_sum %.6f
`
