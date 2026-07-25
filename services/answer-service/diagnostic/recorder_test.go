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

func TestRecorderLogsFixedOutcomeAndProviderResponseMetadata(t *testing.T) {
	var output bytes.Buffer
	log, err := sharedlogger.NewWithWriter(&output)
	if err != nil {
		t.Fatal(err)
	}
	recorder := New(log, &metrics.Recorder{})
	recorder.ProviderStarted()
	recorder.ProviderResponse(2, 17)
	recorder.Failure(application.OutcomeInvalidOutput, "validation", "invalid_provider_output", "provider returned HTTP status 404", 12*time.Millisecond)
	line := output.String()
	if !strings.Contains(line, "answer provider request") || !strings.Contains(line, "answer provider response") || !strings.Contains(line, "segment_count=2") || !strings.Contains(line, "summary_length=17") || !strings.Contains(line, "answer request failed") || !strings.Contains(line, "outcome=invalid_output") || !strings.Contains(line, "stage=validation") || !strings.Contains(line, "reason_code=invalid_provider_output") || !strings.Contains(line, "reason_detail=provider returned HTTP status 404") || !strings.Contains(line, "duration_ms=12") {
		t.Fatalf("log line = %q", line)
	}
	for _, canary := range []string{"grounded response", "question-canary", "passage-canary", "provider-canary", "secret-canary"} {
		if strings.Contains(line, canary) {
			t.Fatalf("log contains %q", canary)
		}
	}
}
