package application

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
)

type fakeRetriever struct {
	result domain.SearchResult
	err    error
	calls  atomic.Int32
}

func (f *fakeRetriever) Search(context.Context, domain.SearchRequest) (domain.SearchResult, error) {
	f.calls.Add(1)
	return f.result, f.err
}
func (f *fakeRetriever) CheckReady(context.Context) error { return f.err }

type fakeProvider struct {
	segments []domain.AnswerSegment
	err      error
	calls    atomic.Int32
	block    <-chan struct{}
	input    ProviderRequest
}

func (f *fakeProvider) Generate(ctx context.Context, input ProviderRequest) ([]domain.AnswerSegment, error) {
	f.calls.Add(1)
	f.input = input
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.segments, f.err
}

func TestAnswerSelectsBoundedDeduplicatedEvidenceInRankingOrder(t *testing.T) {
	limits := DefaultLimits()
	limits.MaximumEvidence = 2
	limits.MaximumEvidenceBytes = 8
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"e-1"}}}}
	search := domain.SearchResult{
		Results:   []domain.Evidence{{EvidenceID: "e-1", Passage: "first"}, {EvidenceID: "oversized", Passage: "this passage is oversized"}},
		Documents: []domain.DocumentResult{{Evidence: []domain.Evidence{{EvidenceID: "e-1", Passage: "duplicate"}, {EvidenceID: "e-2", Passage: "second"}}}},
	}
	service := newTestService(t, &fakeRetriever{result: search}, provider, limits)
	result, err := service.Answer(context.Background(), validRequest())
	if err != nil || result.Answer == nil || len(provider.input.Evidence) != 2 || provider.input.Evidence[0].EvidenceID != "e-1" || provider.input.Evidence[1].EvidenceID != "e-2" {
		t.Fatalf("Answer() = %#v, %v; context=%#v", result, err, provider.input.Evidence)
	}
}

type fakeObserver struct{}

func (fakeObserver) Observe(Outcome, time.Duration) {}
func (fakeObserver) ProviderStarted()               {}
func (fakeObserver) ProviderFinished()              {}

func TestAnswerReturnsValidatedGroundedSegments(t *testing.T) {
	retriever := &fakeRetriever{result: searchResult("evidence-1")}
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: " Grounded answer ", EvidenceIDs: []string{"evidence-1"}}}}
	service := newTestService(t, retriever, provider, DefaultLimits())
	result, err := service.Answer(context.Background(), validRequest())
	if err != nil || result.Answer == nil || result.Answer.Segments[0].Text != "Grounded answer" {
		t.Fatalf("Answer() = %#v, %v", result, err)
	}
}

func TestAnswerDoesNotCallProviderWithoutEvidence(t *testing.T) {
	provider := &fakeProvider{}
	service := newTestService(t, &fakeRetriever{result: domain.SearchResult{}}, provider, DefaultLimits())
	result, err := service.Answer(context.Background(), validRequest())
	if err != nil || result.Answer != nil || provider.calls.Load() != 0 {
		t.Fatalf("Answer() = %#v, %v; calls=%d", result, err, provider.calls.Load())
	}
}

func TestAnswerDegradesForProviderAndCitationFailures(t *testing.T) {
	tests := []*fakeProvider{
		{err: errors.New("provider failed")},
		{segments: []domain.AnswerSegment{{Text: "unsupported", EvidenceIDs: []string{"unknown"}}}},
		{segments: []domain.AnswerSegment{{Text: "duplicate", EvidenceIDs: []string{"evidence-1", "evidence-1"}}}},
	}
	for index, provider := range tests {
		service := newTestService(t, &fakeRetriever{result: searchResult("evidence-1")}, provider, DefaultLimits())
		result, err := service.Answer(context.Background(), validRequest())
		if err != nil || result.Answer != nil || len(result.Search.Results) != 1 {
			t.Fatalf("case %d: %#v, %v", index, result, err)
		}
	}
}

func TestAnswerBoundsContextAndUsesNonBlockingConcurrency(t *testing.T) {
	limits := DefaultLimits()
	limits.ProviderConcurrency = 1
	block := make(chan struct{})
	provider := &fakeProvider{segments: []domain.AnswerSegment{{Text: "answer", EvidenceIDs: []string{"evidence-1"}}}, block: block}
	service := newTestService(t, &fakeRetriever{result: searchResult("evidence-1")}, provider, limits)
	done := make(chan struct{})
	go func() {
		_, _ = service.Answer(context.Background(), validRequest())
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for provider.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	result, err := service.Answer(context.Background(), validRequest())
	if err != nil || result.Answer != nil || time.Since(started) > 100*time.Millisecond || provider.calls.Load() != 1 {
		t.Fatalf("saturated Answer() = %#v, %v; calls=%d", result, err, provider.calls.Load())
	}
	close(block)
	<-done
}

func TestAnswerRejectsOversizedOrMixedValidityOutput(t *testing.T) {
	limits := DefaultLimits()
	limits.MaximumAnswerBytes = 4
	providers := []*fakeProvider{
		{segments: []domain.AnswerSegment{{Text: "large", EvidenceIDs: []string{"evidence-1"}}}},
		{segments: []domain.AnswerSegment{{Text: "ok", EvidenceIDs: []string{"evidence-1"}}, {Text: "bad", EvidenceIDs: []string{"unknown"}}}},
		{segments: []domain.AnswerSegment{{Text: "unsafe\u202eanswer", EvidenceIDs: []string{"evidence-1"}}}},
		{segments: []domain.AnswerSegment{{Text: "unsafe\nanswer", EvidenceIDs: []string{"evidence-1"}}}},
	}
	for _, provider := range providers {
		service := newTestService(t, &fakeRetriever{result: searchResult("evidence-1")}, provider, limits)
		result, err := service.Answer(context.Background(), validRequest())
		if err != nil || result.Answer != nil {
			t.Fatalf("Answer() = %#v, %v", result, err)
		}
	}
}

func newTestService(t *testing.T, retriever Retriever, provider LLMProvider, limits Limits) *Service {
	t.Helper()
	service, err := NewService(retriever, provider, fakeObserver{}, limits)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func validRequest() domain.SearchRequest {
	return domain.SearchRequest{Question: "question", Limit: 5, Actor: domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, CorrelationID: strings.Repeat("a", 32)}
}

func searchResult(id string) domain.SearchResult {
	return domain.SearchResult{Query: "question", Results: []domain.Evidence{{EvidenceID: id, Passage: "trusted evidence"}}}
}
