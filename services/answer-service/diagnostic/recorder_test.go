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

func TestRecorderLogsOnlyFixedOutcomeAndDuration(t *testing.T) {
	var output bytes.Buffer
	log, err := sharedlogger.NewWithWriter(&output)
	if err != nil {
		t.Fatal(err)
	}
	recorder := New(log, &metrics.Recorder{})
	recorder.Observe(application.OutcomeInvalidOutput, 12*time.Millisecond)
	line := output.String()
	if !strings.Contains(line, "answer request degraded") || !strings.Contains(line, "duration_ms=12") {
		t.Fatalf("log line = %q", line)
	}
	for _, canary := range []string{"question-canary", "passage-canary", "provider-canary", "secret-canary"} {
		if strings.Contains(line, canary) {
			t.Fatalf("log contains %q", canary)
		}
	}
}
