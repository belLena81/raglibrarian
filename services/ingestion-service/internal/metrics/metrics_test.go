package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
)

func TestHealthAndReadinessTransitions(t *testing.T) {
	recorder := &Recorder{}
	for _, test := range []struct {
		path string
		want int
	}{{path: "/healthz", want: http.StatusOK}, {path: "/readyz", want: http.StatusServiceUnavailable}} {
		response := httptest.NewRecorder()
		recorder.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s: expected %d, got %d", test.path, test.want, response.Code)
		}
	}
	recorder.SetReadiness(true, true, true)
	response := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected ready, got %d", response.Code)
	}
	recorder.SetReadiness(true, false, true)
	response = httptest.NewRecorder()
	recorder.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected readiness loss, got %d", response.Code)
	}
}

func TestPhaseHistogramHasFixedCardinalityAndBuckets(t *testing.T) {
	recorder := &Recorder{}
	recorder.ObservePhase(application.PhaseDownload, 750*time.Millisecond)
	recorder.ObservePhase(application.ProcessingPhase(99), time.Second)
	response := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	if strings.Contains(body, "minio_ready") {
		t.Fatal("backend-specific readiness metric leaked")
	}
	if got := strings.Count(body, "ingestion_processing_phase_duration_seconds_bucket{"); got != 4*(len(phaseBuckets)+1) {
		t.Fatalf("unexpected bucket cardinality: %d", got)
	}
	for _, phase := range phaseNames {
		if !strings.Contains(body, `phase="`+phase+`"`) {
			t.Fatalf("missing fixed phase %q", phase)
		}
	}
	if strings.Contains(body, "99") {
		t.Fatal("invalid phase created a dynamic series")
	}
}
