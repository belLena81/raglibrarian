// Package diagnostic emits fixed Answer lifecycle outcomes and content-free
// provider response metadata.
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
	r.log.Info(message,
		zap.String("outcome", string(outcome)),
		zap.Int64("duration_ms", duration.Milliseconds()),
	)
}

func (r *Recorder) Failure(outcome application.Outcome, stage, reasonCode string, duration time.Duration) {
	r.metrics.Observe(outcome, duration)
	r.log.Info("answer.request.failed",
		zap.String("outcome", string(outcome)),
		zap.String("stage", stage),
		zap.String("reason_code", reasonCode),
		zap.Int64("duration_ms", duration.Milliseconds()),
	)
}

func (r *Recorder) GeneratorStarted() {
	r.metrics.GeneratorStarted()
	r.log.Info("answer.provider.request")
}

func (r *Recorder) GeneratorResponse(segmentCount, summaryLength int) {
	r.log.Info("answer.provider.response",
		zap.Int("segment_count", segmentCount),
		zap.Int("summary_length", summaryLength),
	)
}

func (r *Recorder) GeneratorFinished() {
	r.metrics.GeneratorFinished()
}

func (r *Recorder) CacheConfigured(state application.CacheState) {
	r.metrics.CacheConfigured(state)
	r.log.Info("answer.cache.configured",
		zap.Bool("cache_enabled", state.Enabled),
		zap.Int("cache_capacity", state.Capacity),
		zap.Int64("cache_ttl_seconds", state.TTLSeconds),
		zap.Int("cache_minimum_cosine_millis", state.MinimumCosineMillis),
	)
}

func (r *Recorder) CacheLookup(diagnostic application.CacheDiagnostic) {
	r.metrics.CacheLookup(diagnostic.Outcome)
	r.log.Info("answer.cache.lookup",
		zap.String("cache_outcome", string(diagnostic.Outcome)),
		zap.String("stage", diagnostic.Stage),
		zap.String("reason_code", diagnostic.Reason),
	)
}

func (r *Recorder) CacheOperational(state application.CacheOperationalState) {
	r.metrics.CacheOperational(state)
}
