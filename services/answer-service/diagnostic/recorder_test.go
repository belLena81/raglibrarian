package diagnostic

import (
	"bytes"
	"strings"
	"testing"
	"time"

	sharedlogger "github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/metrics"
)

func TestRecorderLogsFixedOutcomeAndGeneratorResponseMetadata(t *testing.T) {
	var output bytes.Buffer
	log, err := sharedlogger.NewWithWriter(&output)
	if err != nil {
		t.Fatal(err)
	}
	recorder := New(log, &metrics.Recorder{})
	recorder.CacheConfigured(application.CacheState{
		Enabled:             true,
		Capacity:            8,
		TTLSeconds:          60,
		MinimumCosineMillis: 900,
	})
	recorder.GeneratorStarted()
	recorder.GeneratorResponse(2, 17)
	recorder.CacheLookup(application.CacheDiagnostic{
		Outcome: application.CacheOutcomeHit,
		Stage:   "match",
		Reason:  "hit",
	})
	recorder.Failure(application.OutcomeInvalidOutput, "validation", "invalid_provider_output", 12*time.Millisecond)
	line := output.String()
	if !strings.Contains(line, "answer cache configured") || !strings.Contains(line, "cache_enabled=true") ||
		!strings.Contains(line, "cache_capacity=8") || !strings.Contains(line, "cache_ttl_seconds=60") ||
		!strings.Contains(line, "cache_minimum_cosine_millis=900") ||
		!strings.Contains(line, "answer provider request") || !strings.Contains(line, "answer provider response") ||
		!strings.Contains(line, "segment_count=2") || !strings.Contains(line, "summary_length=17") ||
		!strings.Contains(line, "answer cache lookup") || !strings.Contains(line, "cache_outcome=hit") ||
		!strings.Contains(line, "stage=match") || !strings.Contains(line, "reason_code=hit") ||
		!strings.Contains(line, "answer request failed") || !strings.Contains(line, "outcome=invalid_output") ||
		!strings.Contains(line, "stage=validation") || !strings.Contains(line, "reason_code=invalid_provider_output") ||
		!strings.Contains(line, "duration_ms=12") {
		t.Fatalf("log line = %q", line)
	}
	if strings.Contains(line, "reason_detail") {
		t.Fatalf("log contains unsafe reason detail: %q", line)
	}
	for _, canary := range []string{"grounded response", "question-canary", "passage-canary", "provider-canary", "secret-canary"} {
		if strings.Contains(line, canary) {
			t.Fatalf("log contains %q", canary)
		}
	}
}
