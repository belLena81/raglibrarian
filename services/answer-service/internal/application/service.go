// Package application orchestrates Retrieval and answer generation without transport dependencies.
package application

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	Failure(Outcome, string, string, string, time.Duration)
	GeneratorStarted()
	GeneratorResponse(segmentCount, summaryLength int)
	GeneratorFinished()
}

type codedReason interface {
	ReasonCode() string
}

type detailedReason interface {
	ReasonDetail() string
}

type Limits struct {
	MaximumEvidence           int
	MaximumContextBytes       int
	MaximumEvidenceBytes      int
	MaximumSegments           int
	MaximumAnswerBytes        int
	MaximumSummaryRunes       int
	MaximumFailureDetailRunes int
	MaximumCitations          int
	MaximumOutputTokens       int
	GeneratorConcurrency      int
	RequestTimeout            time.Duration
	RetrievalTimeout          time.Duration
	GeneratorTimeout          time.Duration
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
	if retriever == nil || generator == nil || observer == nil || !validLimits(limits) || requestPolicy.MaximumResultLimit == 0 ||
		requestPolicy.MaximumQuestionCharacters == 0 || requestPolicy.MaximumFilterTags == 0 || requestPolicy.MaximumTagCharacters == 0 ||
		requestPolicy.MaximumAuthorCharacters == 0 {
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
	return l.MaximumEvidence > 0 && l.MaximumEvidence <= 64 && l.MaximumContextBytes > 0 && l.MaximumContextBytes <= 1<<20 &&
		l.MaximumEvidenceBytes > 0 && l.MaximumEvidenceBytes <= l.MaximumContextBytes && l.MaximumSegments > 0 && l.MaximumSegments <= 64 &&
		l.MaximumAnswerBytes > 0 && l.MaximumAnswerBytes <= 1<<20 && l.MaximumSummaryRunes > 0 && l.MaximumSummaryRunes <= 1<<20 &&
		l.MaximumFailureDetailRunes > 0 && l.MaximumFailureDetailRunes <= 1<<20 &&
		l.MaximumCitations > 0 && l.MaximumCitations <= 64 &&
		l.MaximumOutputTokens > 0 && l.MaximumOutputTokens <= 8192 && l.GeneratorConcurrency > 0 && l.GeneratorConcurrency <= 64 &&
		l.RequestTimeout > 0 && l.RetrievalTimeout > 0 && l.GeneratorTimeout > 0 && l.RetrievalTimeout < l.RequestTimeout && l.GeneratorTimeout < l.RequestTimeout
}

func (s *Service) CheckReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.limits.RetrievalTimeout)
	defer cancel()
	return s.retriever.CheckReady(ctx)
}

func (s *Service) Answer(parent context.Context, request domain.SearchRequest) (domain.AnswerResult, error) {
	started := time.Now()
	if err := request.Validate(s.requestPolicy); err != nil {
		return domain.AnswerResult{}, err
	}
	ctx, cancel := context.WithTimeout(parent, s.limits.RequestTimeout)
	defer cancel()
	retrievalContext, retrievalCancel := context.WithTimeout(ctx, s.limits.RetrievalTimeout)
	search, err := s.retriever.Search(retrievalContext, request)
	retrievalCancel()
	if err != nil {
		s.observer.Failure(OutcomeRetrievalFailure, "retrieval", failureReasonCode(err, "retrieval"), failureReasonDetail(err, s.limits.MaximumFailureDetailRunes), time.Since(started))
		return domain.AnswerResult{}, err
	}
	search = filterSearchByMinimumEvidenceScore(search, request.MinimumEvidenceScore)
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
	segments, err := s.generator.Generate(generatorContext, GeneratorRequest{Question: strings.TrimSpace(request.Question), Evidence: evidence,
		MaxTokens: s.limits.MaximumOutputTokens, MaxSegment: s.limits.MaximumSegments})
	generatorCancel()
	if err != nil {
		s.observer.Failure(OutcomeGeneratorFailure, "generator", failureReasonCode(err, "generator"), failureReasonDetail(err, s.limits.MaximumFailureDetailRunes), time.Since(started))
		return result, nil
	}
	validated, err := validateSegments(segments, evidence, s.limits)
	if err != nil {
		s.observer.Failure(OutcomeInvalidOutput, "validation", failureReasonCode(err, "validation"), failureReasonDetail(err, s.limits.MaximumFailureDetailRunes), time.Since(started))
		return result, nil
	}
	summary := summarizeSegments(validated, s.limits.MaximumSummaryRunes)
	result.Answer = &domain.GroundedAnswer{Segments: validated}
	result.Summary = summary
	s.observer.GeneratorResponse(len(validated), utf8.RuneCountInString(summary))
	s.observer.Observe(OutcomeAnswered, time.Since(started))
	return result, nil
}

func filterSearchByMinimumEvidenceScore(search domain.SearchResult, minimumEvidenceScore float64) domain.SearchResult {
	if minimumEvidenceScore <= 0 || math.IsNaN(minimumEvidenceScore) || math.IsInf(minimumEvidenceScore, 0) {
		return search
	}
	results := make([]domain.Evidence, 0, len(search.Results))
	for _, value := range search.Results {
		if value.Score >= minimumEvidenceScore {
			results = append(results, value)
		}
	}
	documents := make([]domain.DocumentResult, 0, len(search.Documents))
	for _, document := range search.Documents {
		evidence := make([]domain.Evidence, 0, len(document.Evidence))
		for _, value := range document.Evidence {
			if value.Score >= minimumEvidenceScore {
				evidence = append(evidence, value)
			}
		}
		if len(evidence) == 0 {
			continue
		}
		document.Evidence = evidence
		documents = append(documents, document)
	}
	search.Results = results
	search.Documents = documents
	return search
}

func selectEvidence(search domain.SearchResult, limits Limits) []domain.ContextEvidence {
	selected := make([]domain.ContextEvidence, 0, 1)
	seen := make(map[string]struct{})
	total := 0
	add := func(value domain.Evidence) bool {
		if len(selected) >= 1 || value.EvidenceID == "" || !utf8.ValidString(value.Passage) || len(value.Passage) == 0 || len(value.Passage) > limits.MaximumEvidenceBytes {
			return false
		}
		if _, found := seen[value.EvidenceID]; found || total+len(value.Passage) > limits.MaximumContextBytes {
			return false
		}
		seen[value.EvidenceID] = struct{}{}
		total += len(value.Passage)
		selected = append(selected, contextEvidence(value))
		return true
	}
	for _, value := range search.Results {
		if add(value) {
			return selected
		}
	}
	for _, document := range search.Documents {
		for _, value := range document.Evidence {
			if add(value) {
				return selected
			}
		}
	}
	return selected
}

func contextEvidence(value domain.Evidence) domain.ContextEvidence {
	return domain.ContextEvidence{
		EvidenceID: value.EvidenceID,
		Passage:    value.Passage,
		Title:      value.Book.Title,
		Author:     value.Book.Author,
		Chapter:    value.Chapter,
		Section:    value.Section,
		PageStart:  value.PageStart,
		PageEnd:    value.PageEnd,
	}
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
		groundedText := groundedSegmentText(text, value.EvidenceIDs, evidence)
		total += len(groundedText)
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
		result = append(result, domain.AnswerSegment{Text: groundedText, EvidenceIDs: citations})
	}
	return result, nil
}

func groundedSegmentText(text string, evidenceIDs []string, evidence []domain.ContextEvidence) string {
	if len(evidenceIDs) == 0 {
		return text
	}
	var selected domain.ContextEvidence
	for _, value := range evidence {
		if value.EvidenceID == evidenceIDs[0] {
			selected = value
			break
		}
	}
	title := humanField(selected.Title)
	if title == "" {
		return text
	}
	pages := pageRange(selected.PageStart, selected.PageEnd)
	location := title
	if author := humanField(selected.Author); author != "" {
		location += " by " + author
	}
	if pages != "" {
		location += ", " + pages
	}
	return "This is described in " + location + ": " + text
}

func humanField(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(char rune) bool {
		return unicode.IsSpace(char) || unicode.IsControl(char) || unicode.Is(unicode.Cf, char)
	}), " ")
}

func pageRange(start, end uint32) string {
	if start == 0 && end == 0 {
		return ""
	}
	if end == 0 || end == start {
		return fmt.Sprintf("page %d", start)
	}
	return fmt.Sprintf("pages %d-%d", start, end)
}

func unsafeAnswerRune(value rune) bool {
	return unicode.IsControl(value) || unicode.Is(unicode.Cf, value) || value == '\u2028' || value == '\u2029'
}

func failureReasonCode(err error, stage string) string {
	if err == nil {
		return "unknown_failure"
	}
	var coded codedReason
	if errors.As(err, &coded) {
		return coded.ReasonCode()
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case stage == "generator" && strings.Contains(err.Error(), "provider unavailable"):
		return "provider_unavailable"
	case stage == "generator" && strings.Contains(err.Error(), "invalid provider response"):
		return "invalid_provider_response"
	case stage == "validation" && strings.Contains(err.Error(), "invalid provider output"):
		return "invalid_provider_output"
	case stage == "retrieval":
		return "retrieval_failed"
	default:
		return "unknown_failure"
	}
}

func failureReasonDetail(err error, maximumFailureDetailRunes int) string {
	if err == nil {
		return ""
	}
	var detailed detailedReason
	if errors.As(err, &detailed) {
		return detailed.ReasonDetail()
	}
	return sanitizeFailureDetail(err.Error(), maximumFailureDetailRunes)
}

func sanitizeFailureDetail(value string, maximumFailureDetailRunes int) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) > maximumFailureDetailRunes {
		runes := []rune(value)
		value = string(runes[:maximumFailureDetailRunes])
	}
	return value
}

func summarizeSegments(values []domain.AnswerSegment, maximumSummaryRunes int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	summary := strings.Join(parts, " ")
	summary = strings.Join(strings.Fields(summary), " ")
	if utf8.RuneCountInString(summary) <= maximumSummaryRunes {
		return summary
	}
	runes := []rune(summary)
	return strings.TrimSpace(string(runes[:maximumSummaryRunes-1])) + "…"
}
