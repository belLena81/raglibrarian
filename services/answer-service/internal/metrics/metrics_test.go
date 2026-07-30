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
	recorder.CacheLookup(application.CacheOutcomeHit)
	recorder.CacheLookup(application.CacheOutcomeLexicalHit)
	recorder.CacheLookup(application.CacheOutcomeSemanticOnlyHit)
	recorder.CacheLookup(application.CacheOutcomeGuardMismatch)
	metricsResponse := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metricsResponse.Body.String(), `answer_requests_total{outcome="answered"} 1`) {
		t.Fatalf("metrics = %q", metricsResponse.Body.String())
	}
	if !strings.Contains(metricsResponse.Body.String(), `answer_cache_lookups_total{outcome="hit"} 1`) {
		t.Fatalf("metrics = %q", metricsResponse.Body.String())
	}
	if !strings.Contains(metricsResponse.Body.String(), `answer_cache_lookups_total{outcome="lexical_hit"} 1`) ||
		!strings.Contains(metricsResponse.Body.String(), `answer_cache_lookups_total{outcome="semantic_only_hit"} 1`) ||
		!strings.Contains(metricsResponse.Body.String(), `answer_cache_lookups_total{outcome="guard_mismatch"} 1`) {
		t.Fatalf("metrics = %q", metricsResponse.Body.String())
	}
}
