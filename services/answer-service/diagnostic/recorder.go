// Package diagnostic emits fixed, content-free Answer lifecycle outcomes.
package diagnostic

import (
	"time"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/metrics"
	"go.uber.org/zap"
)

type Recorder struct {
	log     *zap.Logger
	metrics *metrics.Recorder
}

func New(log *zap.Logger, metricRecorder *metrics.Recorder) *Recorder {
	if log == nil || metricRecorder == nil {
		panic("diagnostic: logger and metrics are required")
	}
	return &Recorder{log: log, metrics: metricRecorder}
}

func (r *Recorder) Observe(outcome application.Outcome, duration time.Duration) {
	r.metrics.Observe(outcome, duration)
	message := "answer.request.degraded"
	if outcome == application.OutcomeAnswered {
		message = "answer.request.completed"
	}
	r.log.Info(message, zap.Int64("duration_ms", duration.Milliseconds()))
}

func (r *Recorder) ProviderStarted() {
	r.metrics.ProviderStarted()
}

func (r *Recorder) ProviderFinished() {
	r.metrics.ProviderFinished()
}
