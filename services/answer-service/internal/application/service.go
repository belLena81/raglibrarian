// Package application orchestrates Retrieval and answer generation without transport dependencies.
package application

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
)

type Retriever interface {
	Search(context.Context, domain.SearchRequest) (domain.SearchResult, error)
	CheckReady(context.Context) error
}

type GeneratorRequest struct {
	Question   string
	Evidence   []domain.ContextEvidence
	MaxTokens  int
	MaxSegment int
}

type AnswerGenerator interface {
	Generate(context.Context, GeneratorRequest) ([]domain.AnswerSegment, error)
}

type Outcome string

const (
	OutcomeAnswered          Outcome = "answered"
	OutcomeEmptyEvidence     Outcome = "empty_evidence"
	OutcomeRetrievalFailure  Outcome = "retrieval_failure"
	OutcomeGeneratorFailure  Outcome = "provider_failure"
	OutcomeInvalidOutput     Outcome = "invalid_output"
	OutcomeCapacityExhausted Outcome = "capacity_exhausted"
)

type Observer interface {
	Observe(Outcome, time.Duration)
	Failure(Outcome, string, string, time.Duration)
	GeneratorStarted()
	GeneratorResponse(segmentCount, summaryLength int)
	GeneratorFinished()
	CacheLookup(CacheOutcome)
}

type codedReason interface {
	ReasonCode() string
}

type Limits struct {
	MaximumEvidence      int
	MaximumContextBytes  int
	MaximumEvidenceBytes int
	MaximumSegments      int
	MaximumAnswerBytes   int
	MaximumSummaryRunes  int
	MaximumCitations     int
	MaximumOutputTokens  int
	GeneratorConcurrency int
	RequestTimeout       time.Duration
	RetrievalTimeout     time.Duration
	GeneratorTimeout     time.Duration
}

type Service struct {
	retriever     Retriever
	generator     AnswerGenerator
	observer      Observer
	limits        Limits
	requestPolicy domain.RequestPolicy
	permits       chan struct{}
	cache         *answerCache
	inflight      *answerInflight
}

// NewService constructs an Answer service. A missing cache policy keeps the
// final-answer cache disabled, preserving the previous runtime behaviour.
func NewService(retriever Retriever, generator AnswerGenerator, observer Observer, limits Limits, requestPolicy domain.RequestPolicy, cachePolicies ...CachePolicy) (*Service, error) {
	if retriever == nil ||
		generator == nil ||
		observer == nil ||
		!validLimits(limits) ||
		!validRequestPolicy(requestPolicy) ||
		len(cachePolicies) > 1 {
		return nil, errors.New("invalid answer service configuration")
	}
	service := &Service{
		retriever:     retriever,
		generator:     generator,
		observer:      observer,
		limits:        limits,
		requestPolicy: requestPolicy,
		permits:       make(chan struct{}, limits.GeneratorConcurrency),
		inflight:      newAnswerInflight(),
	}
	if len(cachePolicies) == 1 {
		cache, err := newAnswerCache(cachePolicies[0])
		if err != nil {
			return nil, errors.New("invalid answer service configuration")
		}
		service.cache = cache
	}
	return service, nil
}

func validLimits(l Limits) bool {
	return l.MaximumEvidence > 0 &&
		l.MaximumEvidence <= 64 &&
		l.MaximumContextBytes > 0 &&
		l.MaximumContextBytes <= 1<<20 &&
		l.MaximumEvidenceBytes > 0 &&
		l.MaximumEvidenceBytes <= l.MaximumContextBytes &&
		l.MaximumSegments > 0 &&
		l.MaximumSegments <= 64 &&
		l.MaximumAnswerBytes > 0 &&
		l.MaximumAnswerBytes <= 1<<20 &&
		l.MaximumSummaryRunes > 0 &&
		l.MaximumSummaryRunes <= 1<<20 &&
		l.MaximumCitations > 0 &&
		l.MaximumCitations <= 64 &&
		l.MaximumOutputTokens > 0 &&
		l.MaximumOutputTokens <= 8192 &&
		l.GeneratorConcurrency > 0 &&
		l.GeneratorConcurrency <= 64 &&
		l.RequestTimeout > 0 &&
		l.RetrievalTimeout > 0 &&
		l.GeneratorTimeout > 0 &&
		l.RetrievalTimeout < l.RequestTimeout &&
		l.GeneratorTimeout < l.RequestTimeout
}

func validRequestPolicy(policy domain.RequestPolicy) bool {
	return policy.MaximumQuestionCharacters > 0 &&
		policy.MaximumFilterTags > 0 &&
		policy.MaximumTagCharacters > 0 &&
		policy.MaximumAuthorCharacters > 0 &&
		policy.MaximumResultLimit > 0
}

func (s *Service) CheckReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.limits.RetrievalTimeout)
	defer cancel()
	return s.retriever.CheckReady(ctx)
}

func (s *Service) Answer(parent context.Context, request domain.SearchRequest) (domain.AnswerResult, error) {
	started := time.Now()
	normalized, err := request.NormalizeAndValidate(s.requestPolicy)
	if err != nil {
		return domain.AnswerResult{}, err
	}
	ctx, cancel := context.WithTimeout(parent, s.limits.RequestTimeout)
	defer cancel()
	retrievalContext, retrievalCancel := context.WithTimeout(ctx, s.limits.RetrievalTimeout)
	search, err := s.retriever.Search(retrievalContext, normalized)
	retrievalCancel()
	if err != nil {
		s.observer.Failure(OutcomeRetrievalFailure, "retrieval", failureReasonCode(err, "retrieval"), time.Since(started))
		return domain.AnswerResult{}, err
	}
	search = filterSearchByMinimumEvidenceScore(search, normalized.MinimumEvidenceScore)
	result := domain.AnswerResult{Search: search}
	evidence := selectEvidence(search, s.limits)
	if len(evidence) == 0 {
		s.observer.Observe(OutcomeEmptyEvidence, time.Since(started))
		return result, nil
	}
	if cached, outcome := s.cache.lookup(normalized, search, evidence); cacheMatchHit(outcome) {
		validated, validationErr := validateSegments(cached.segments, evidence, s.limits)
		if validationErr == nil {
			summary := summarizeSegments(validated, s.limits.MaximumSummaryRunes)
			result.Answer = &domain.GroundedAnswer{Segments: validated}
			result.Summary = summary
			s.observer.CacheLookup(outcome)
			s.observer.GeneratorResponse(len(validated), utf8.RuneCountInString(summary))
			s.observer.Observe(OutcomeAnswered, time.Since(started))
			return result, nil
		}
		s.observer.CacheLookup(CacheOutcomeValidationMismatch)
	} else {
		s.observer.CacheLookup(outcome)
	}
	flightKey, coalesce := s.cache.flightKey(normalized, search, evidence)
	if coalesce {
		if segments, ok, waited, waitErr := s.inflight.wait(ctx, flightKey); waitErr != nil {
			return result, waitErr
		} else if waited && ok {
			validated, validationErr := validateSegments(segments, evidence, s.limits)
			if validationErr == nil {
				summary := summarizeSegments(validated, s.limits.MaximumSummaryRunes)
				result.Answer = &domain.GroundedAnswer{Segments: validated}
				result.Summary = summary
				s.observer.CacheLookup(CacheOutcomeGenerationCoalesced)
				s.observer.GeneratorResponse(len(validated), utf8.RuneCountInString(summary))
				s.observer.Observe(OutcomeAnswered, time.Since(started))
				return result, nil
			}
			s.observer.CacheLookup(CacheOutcomeValidationMismatch)
		} else if waited {
			return result, nil
		}
		defer s.inflight.done(flightKey)()
	}
	select {
	case s.permits <- struct{}{}:
	default:
		s.observer.Observe(OutcomeCapacityExhausted, time.Since(started))
		return result, nil
	}
	s.observer.GeneratorStarted()
	defer func() {
		<-s.permits
		s.observer.GeneratorFinished()
	}()
	generatorContext, generatorCancel := context.WithTimeout(ctx, s.limits.GeneratorTimeout)
	segments, err := s.generator.Generate(generatorContext, GeneratorRequest{
		Question:   normalized.Question,
		Evidence:   evidence,
		MaxTokens:  s.limits.MaximumOutputTokens,
		MaxSegment: s.limits.MaximumSegments,
	})
	generatorCancel()
	if err != nil {
		s.observer.Failure(OutcomeGeneratorFailure, "generator", failureReasonCode(err, "generator"), time.Since(started))
		if coalesce {
			s.inflight.complete(flightKey, nil, false)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, nil
	}
	validated, err := validateSegments(segments, evidence, s.limits)
	if err != nil {
		s.observer.Failure(OutcomeInvalidOutput, "validation", failureReasonCode(err, "validation"), time.Since(started))
		if coalesce {
			s.inflight.complete(flightKey, nil, false)
		}
		return result, nil
	}
	summary := summarizeSegments(validated, s.limits.MaximumSummaryRunes)
	result.Answer = &domain.GroundedAnswer{Segments: validated}
	result.Summary = summary
	s.cache.store(normalized, search, evidence, segments)
	if coalesce {
		s.inflight.complete(flightKey, segments, true)
	}
	s.observer.GeneratorResponse(len(validated), utf8.RuneCountInString(summary))
	s.observer.Observe(OutcomeAnswered, time.Since(started))
	return result, nil
}

type answerInflight struct {
	mu    sync.Mutex
	calls map[cacheFlightKey]*answerInflightCall
}

type answerInflightCall struct {
	ready    chan struct{}
	segments []domain.AnswerSegment
	ok       bool
}

func newAnswerInflight() *answerInflight {
	return &answerInflight{calls: make(map[cacheFlightKey]*answerInflightCall)}
}

func (i *answerInflight) wait(ctx context.Context, key cacheFlightKey) ([]domain.AnswerSegment, bool, bool, error) {
	i.mu.Lock()
	if call, found := i.calls[key]; found {
		ready := call.ready
		i.mu.Unlock()
		select {
		case <-ready:
			return cloneSegments(call.segments), call.ok, true, nil
		case <-ctx.Done():
			return nil, false, true, ctx.Err()
		}
	}
	i.calls[key] = &answerInflightCall{ready: make(chan struct{})}
	i.mu.Unlock()
	return nil, false, false, nil
}

func (i *answerInflight) complete(key cacheFlightKey, segments []domain.AnswerSegment, ok bool) {
	i.mu.Lock()
	call, found := i.calls[key]
	if found {
		call.segments = cloneSegments(segments)
		call.ok = ok
	}
	i.mu.Unlock()
	if found {
		close(call.ready)
	}
}

func (i *answerInflight) done(key cacheFlightKey) func() {
	return func() {
		i.mu.Lock()
		call, found := i.calls[key]
		if found {
			select {
			case <-call.ready:
			default:
				close(call.ready)
			}
			delete(i.calls, key)
		}
		i.mu.Unlock()
	}
}
