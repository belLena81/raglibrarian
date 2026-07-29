// Package application orchestrates Retrieval and answer generation without transport dependencies.
package application

import (
	"context"
	"errors"
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
}

func NewService(retriever Retriever, generator AnswerGenerator, observer Observer, limits Limits, requestPolicy domain.RequestPolicy) (*Service, error) {
	if retriever == nil ||
		generator == nil ||
		observer == nil ||
		!validLimits(limits) ||
		!validRequestPolicy(requestPolicy) {
		return nil, errors.New("invalid answer service configuration")
	}
	return &Service{
		retriever:     retriever,
		generator:     generator,
		observer:      observer,
		limits:        limits,
		requestPolicy: requestPolicy,
		permits:       make(chan struct{}, limits.GeneratorConcurrency),
	}, nil
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, nil
	}
	validated, err := validateSegments(segments, evidence, s.limits)
	if err != nil {
		s.observer.Failure(OutcomeInvalidOutput, "validation", failureReasonCode(err, "validation"), time.Since(started))
		return result, nil
	}
	summary := summarizeSegments(validated, s.limits.MaximumSummaryRunes)
	result.Answer = &domain.GroundedAnswer{Segments: validated}
	result.Summary = summary
	s.observer.GeneratorResponse(len(validated), utf8.RuneCountInString(summary))
	s.observer.Observe(OutcomeAnswered, time.Since(started))
	return result, nil
}
