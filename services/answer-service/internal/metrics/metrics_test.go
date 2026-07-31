package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
)

func TestHandlerReportsPrivateHealthReadinessAndFixedMetrics(t *testing.T) {
	recorder := &Recorder{}
	ready := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial readiness = %d", ready.Code)
	}
	recorder.SetRetrievalReady(true)
	recorder.Observe(application.OutcomeAnswered, time.Second)
	recorder.CacheConfigured(application.CacheState{
		Enabled:             true,
		Capacity:            8,
		TTLSeconds:          60,
		MinimumCosineMillis: 900,
	})
	recorder.CacheOperational(application.CacheOperationalState{
		Entries:   1,
		Stores:    2,
		Evictions: 1,
		Expired:   3,
		Sequence:  2,
	})
	recorder.CacheOperational(application.CacheOperationalState{Entries: 0, Sequence: 1})
	recorder.CacheLookup(application.CacheOutcomeHit)
	recorder.CacheLookup(application.CacheOutcomeGuardMismatch)
	recorder.GeneratorStarted()
	recorder.GeneratorFinished()
	metricsResponse := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metricsResponse.Body.String(), `answer_requests_total{outcome="answered"} 1`) {
		t.Fatalf("metrics = %q", metricsResponse.Body.String())
	}
	if !strings.Contains(metricsResponse.Body.String(), `answer_cache_lookups_total{outcome="hit"} 1`) {
		t.Fatalf("metrics = %q", metricsResponse.Body.String())
	}
	if !strings.Contains(metricsResponse.Body.String(), `answer_cache_lookups_total{outcome="guard_mismatch"} 1`) ||
		!strings.Contains(metricsResponse.Body.String(), "answer_cache_enabled 1") ||
		!strings.Contains(metricsResponse.Body.String(), "answer_cache_capacity 8") ||
		!strings.Contains(metricsResponse.Body.String(), "answer_cache_ttl_seconds 60") ||
		!strings.Contains(metricsResponse.Body.String(), "answer_cache_minimum_cosine_millis 900") ||
		!strings.Contains(metricsResponse.Body.String(), "answer_cache_entries 1") ||
		!strings.Contains(metricsResponse.Body.String(), "answer_cache_stores_total 2") ||
		!strings.Contains(metricsResponse.Body.String(), "answer_cache_evictions_total 1") ||
		!strings.Contains(metricsResponse.Body.String(), "answer_cache_expired_entries_total 3") ||
		!strings.Contains(metricsResponse.Body.String(), "answer_provider_requests_total 1") {
		t.Fatalf("metrics = %q", metricsResponse.Body.String())
	}
}
