// Package application orchestrates Retrieval and provider calls without transport dependencies.
package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
)

type Retriever interface {
	Search(context.Context, domain.SearchRequest) (domain.SearchResult, error)
	CheckReady(context.Context) error
}

type ProviderRequest struct {
	Question   string
	Evidence   []domain.ContextEvidence
	MaxTokens  int
	MaxSegment int
}

type LLMProvider interface {
	Generate(context.Context, ProviderRequest) ([]domain.AnswerSegment, error)
}

type Outcome string

const (
	OutcomeAnswered          Outcome = "answered"
	OutcomeEmptyEvidence     Outcome = "empty_evidence"
	OutcomeProviderFailure   Outcome = "provider_failure"
	OutcomeInvalidOutput     Outcome = "invalid_output"
	OutcomeCapacityExhausted Outcome = "capacity_exhausted"
)

type Observer interface {
	Observe(Outcome, time.Duration)
	ProviderStarted()
	ProviderFinished()
}

type Limits struct {
	MaximumEvidence      int
	MaximumContextBytes  int
	MaximumEvidenceBytes int
	MaximumSegments      int
	MaximumAnswerBytes   int
	MaximumCitations     int
	MaximumOutputTokens  int
	ProviderConcurrency  int
	RequestTimeout       time.Duration
	RetrievalTimeout     time.Duration
	ProviderTimeout      time.Duration
}

func DefaultLimits() Limits {
	return Limits{MaximumEvidence: 8, MaximumContextBytes: 32 << 10, MaximumEvidenceBytes: 8 << 10, MaximumSegments: 8,
		MaximumAnswerBytes: 8 << 10, MaximumCitations: 8, MaximumOutputTokens: 768, ProviderConcurrency: 4,
		RequestTimeout: 8 * time.Second, RetrievalTimeout: 3 * time.Second, ProviderTimeout: 4 * time.Second}
}

type Service struct {
	retriever Retriever
	provider  LLMProvider
	observer  Observer
	limits    Limits
	permits   chan struct{}
}

func NewService(retriever Retriever, provider LLMProvider, observer Observer, limits Limits) (*Service, error) {
	if retriever == nil || provider == nil || observer == nil || !validLimits(limits) {
		return nil, errors.New("invalid answer service configuration")
	}
	return &Service{retriever: retriever, provider: provider, observer: observer, limits: limits, permits: make(chan struct{}, limits.ProviderConcurrency)}, nil
}

func validLimits(l Limits) bool {
	return l.MaximumEvidence > 0 && l.MaximumEvidence <= 64 && l.MaximumContextBytes > 0 && l.MaximumContextBytes <= 1<<20 &&
		l.MaximumEvidenceBytes > 0 && l.MaximumEvidenceBytes <= l.MaximumContextBytes && l.MaximumSegments > 0 && l.MaximumSegments <= 64 &&
		l.MaximumAnswerBytes > 0 && l.MaximumAnswerBytes <= 1<<20 && l.MaximumCitations > 0 && l.MaximumCitations <= 64 &&
		l.MaximumOutputTokens > 0 && l.MaximumOutputTokens <= 8192 && l.ProviderConcurrency > 0 && l.ProviderConcurrency <= 64 &&
		l.RequestTimeout > 0 && l.RetrievalTimeout > 0 && l.ProviderTimeout > 0 && l.RetrievalTimeout < l.RequestTimeout && l.ProviderTimeout < l.RequestTimeout
}

func (s *Service) CheckReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.limits.RetrievalTimeout)
	defer cancel()
	return s.retriever.CheckReady(ctx)
}

func (s *Service) Answer(parent context.Context, request domain.SearchRequest) (domain.AnswerResult, error) {
	started := time.Now()
	if err := request.Validate(); err != nil {
		return domain.AnswerResult{}, err
	}
	ctx, cancel := context.WithTimeout(parent, s.limits.RequestTimeout)
	defer cancel()
	retrievalContext, retrievalCancel := context.WithTimeout(ctx, s.limits.RetrievalTimeout)
	search, err := s.retriever.Search(retrievalContext, request)
	retrievalCancel()
	if err != nil {
		return domain.AnswerResult{}, err
	}
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
	s.observer.ProviderStarted()
	defer func() {
		<-s.permits
		s.observer.ProviderFinished()
	}()
	providerContext, providerCancel := context.WithTimeout(ctx, s.limits.ProviderTimeout)
	segments, err := s.provider.Generate(providerContext, ProviderRequest{Question: strings.TrimSpace(request.Question), Evidence: evidence,
		MaxTokens: s.limits.MaximumOutputTokens, MaxSegment: s.limits.MaximumSegments})
	providerCancel()
	if err != nil {
		s.observer.Observe(OutcomeProviderFailure, time.Since(started))
		return result, nil
	}
	validated, err := validateSegments(segments, evidence, s.limits)
	if err != nil {
		s.observer.Observe(OutcomeInvalidOutput, time.Since(started))
		return result, nil
	}
	result.Answer = &domain.GroundedAnswer{Segments: validated}
	s.observer.Observe(OutcomeAnswered, time.Since(started))
	return result, nil
}

func selectEvidence(search domain.SearchResult, limits Limits) []domain.ContextEvidence {
	selected := make([]domain.ContextEvidence, 0, limits.MaximumEvidence)
	seen := make(map[string]struct{})
	total := 0
	add := func(value domain.Evidence) {
		if len(selected) >= limits.MaximumEvidence || value.EvidenceID == "" || !utf8.ValidString(value.Passage) || len(value.Passage) == 0 || len(value.Passage) > limits.MaximumEvidenceBytes {
			return
		}
		if _, found := seen[value.EvidenceID]; found || total+len(value.Passage) > limits.MaximumContextBytes {
			return
		}
		seen[value.EvidenceID] = struct{}{}
		total += len(value.Passage)
		selected = append(selected, domain.ContextEvidence{EvidenceID: value.EvidenceID, Passage: value.Passage})
	}
	for _, value := range search.Results {
		add(value)
	}
	for _, document := range search.Documents {
		for _, value := range document.Evidence {
			add(value)
		}
	}
	return selected
}

func validateSegments(values []domain.AnswerSegment, evidence []domain.ContextEvidence, limits Limits) ([]domain.AnswerSegment, error) {
	if len(values) == 0 || len(values) > limits.MaximumSegments {
		return nil, errors.New("invalid provider output")
	}
	allowed := make(map[string]struct{}, len(evidence))
	for _, value := range evidence {
		allowed[value.EvidenceID] = struct{}{}
	}
	total := 0
	result := make([]domain.AnswerSegment, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value.Text)
		if text == "" || !utf8.ValidString(text) || strings.ContainsRune(text, utf8.RuneError) || strings.IndexFunc(text, unsafeAnswerRune) >= 0 ||
			len(value.EvidenceIDs) == 0 || len(value.EvidenceIDs) > limits.MaximumCitations {
			return nil, errors.New("invalid provider output")
		}
		total += len(text)
		if total > limits.MaximumAnswerBytes {
			return nil, errors.New("invalid provider output")
		}
		seen := make(map[string]struct{}, len(value.EvidenceIDs))
		citations := make([]string, 0, len(value.EvidenceIDs))
		for _, id := range value.EvidenceIDs {
			if _, found := allowed[id]; !found {
				return nil, errors.New("invalid provider output")
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, errors.New("invalid provider output")
			}
			seen[id] = struct{}{}
			citations = append(citations, id)
		}
		result = append(result, domain.AnswerSegment{Text: text, EvidenceIDs: citations})
	}
	return result, nil
}

func unsafeAnswerRune(value rune) bool {
	return unicode.IsControl(value) || unicode.Is(unicode.Cf, value) || value == '\u2028' || value == '\u2029'
}
