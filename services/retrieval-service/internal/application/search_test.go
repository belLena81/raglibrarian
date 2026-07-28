package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

func testSearchPolicy(summaryCallLimit int) SearchPolicy {
	return SearchPolicy{
		MinimumVisibleScore:         0.6,
		AssessmentCallLimit:         summaryCallLimit,
		CandidatePageMultiplier:     2,
		ReciprocalRankFusionK:       60,
		MaximumAssessmentInputRunes: 4096,
		RequestPolicy: domain.SearchRequestPolicy{
			MaximumQuestionCharacters: 2000,
			MaximumFilterTags:         20,
			MaximumTagCharacters:      64,
			MaximumAuthorCharacters:   256,
			DefaultResultLimit:        5,
			MaximumResultLimit:        20,
		},
	}
}

func newTestSearcher(
	t *testing.T,
	embedder QueryEmbedder,
	store EvidenceStore,
	visibility IndexVisibility,
	summaryCallLimit int,
) *Searcher {
	t.Helper()
	searcher, err := NewSearcherWithPolicy(embedder, store, visibility, nil, testSearchPolicy(summaryCallLimit))
	if err != nil {
		t.Fatalf("NewSearcherWithPolicy() error = %v", err)
	}
	return searcher
}

func newTestSearcherWithLexical(
	t *testing.T,
	embedder QueryEmbedder,
	store EvidenceStore,
	lexicalStore LexicalEvidenceStore,
	visibility IndexVisibility,
	summaryCallLimit int,
) *Searcher {
	t.Helper()
	searcher, err := NewSearcherWithPolicyAndLexical(embedder, store, lexicalStore, visibility, nil, testSearchPolicy(summaryCallLimit))
	if err != nil {
		t.Fatalf("NewSearcherWithPolicyAndLexical() error = %v", err)
	}
	return searcher
}

func TestSearcherAuthorizesBeforeCallingDependencies(t *testing.T) {
	embedder := &stubEmbedder{}
	store := &stubEvidenceStore{}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 4)
	var err error
	_, err = searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "pending"}, domain.SearchQueryInput{Question: "replication"})
	if !errors.Is(err, ErrSearchForbidden) {
		t.Fatalf("Search() error = %v, want ErrSearchForbidden", err)
	}
	if embedder.calls != 0 || store.calls != 0 {
		t.Fatalf("dependencies called before authorization: embedder=%d store=%d", embedder.calls, store.calls)
	}
}

func TestSearcherReturnsRankedEvidence(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		results:   []Evidence{{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Title: "Systems", Passage: "Replication keeps copies.", Score: 0.91}},
		documents: []DocumentResult{{DocumentID: "document-1", JobID: "job-1", BookID: "book-1", Title: "Systems", ChunkCount: 10, Score: 0.91, Evidence: []Evidence{{EvidenceID: "evidence-1", Passage: "Replication keeps copies.", Score: 0.91}}}},
	}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 4)
	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: " replication ", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "evidence-1" || result.Evidence[0].Summary != "Replication keeps copies." ||
		len(result.Documents) != 1 || result.Documents[0].DocumentID != "book-1:job-1" || result.Documents[0].Summary != "" ||
		store.query.Question() != "replication" || embedder.calls != 1 {
		t.Fatalf("unexpected results: %#v", result)
	}
}

func TestSearcherUsesProviderAssessmentsAndDoesNotSummarizeDocuments(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		results: []Evidence{{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Title: "Systems", Passage: "Deterministic retries keep search stable.", Score: 0.91}},
		documents: []DocumentResult{{
			DocumentID: "document-1", JobID: "job-1", BookID: "book-1", Title: "Systems", ChunkCount: 1, Score: 0.91,
			Evidence: []Evidence{{EvidenceID: "evidence-1", Passage: "Deterministic retries keep search stable.", Score: 0.91}},
		}},
	}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 4)
	assessor := &stubEvidenceAssessor{response: func(value SummaryRequest) EvidenceAssessment {
		return EvidenceAssessment{Relevant: true, Summary: "summary: " + strings.TrimSpace(value.Question) + " | " + strings.TrimSpace(value.Passage)}
	}}
	searcher.SetEvidenceAssessor(assessor)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: " replication ", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Summary != "summary: replication | Deterministic retries keep search stable." {
		t.Fatalf("provider-backed evidence summary missing: %#v", result.Evidence)
	}
	if len(result.Documents) != 1 || result.Documents[0].Summary != "" || len(result.Documents[0].Evidence) != 1 ||
		result.Documents[0].Evidence[0].Summary != "summary: replication | Deterministic retries keep search stable." {
		t.Fatalf("document grouping should contain only accepted evidence without book summary: %#v", result.Documents)
	}
	if assessor.calls() != 1 {
		t.Fatalf("assessor calls = %d, want 1", assessor.calls())
	}
	if len(assessor.requests) != 1 || assessor.requests[0].Question != "replication" || assessor.requests[0].Passage != "Deterministic retries keep search stable." {
		t.Fatalf("assessor requests missing question/passage: %#v", assessor.requests)
	}
}

func TestSearcherFallsBackToLocalAssessmentsAfterProviderFailure(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		results: []Evidence{
			{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Title: "Systems", Passage: "Provider must assess this passage.", Score: 0.91},
			{EvidenceID: "evidence-2", JobID: "job-2", BookID: "book-2", Title: "Systems", Passage: "Keep local evidence after failure.", Score: 0.90},
		},
	}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 4)

	assessor := &stubEvidenceAssessor{err: errors.New("provider failed")}
	searcher.SetEvidenceAssessor(assessor)
	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 2 {
		t.Fatalf("local fallback evidence = %#v", result.Evidence)
	}
	got := []string{result.Evidence[0].EvidenceID, result.Evidence[1].EvidenceID}
	if got[0] != "evidence-1" || got[1] != "evidence-2" {
		t.Fatalf("local fallback evidence = %#v", result.Evidence)
	}
	if result.Evidence[0].Summary != "Provider must assess this passage." || result.Evidence[1].Summary != "Keep local evidence after failure." {
		t.Fatalf("local fallback summaries = %#v", result.Evidence)
	}
	if assessor.calls() != 1 {
		t.Fatalf("assessor calls = %d, want 1 after first failure", assessor.calls())
	}
}

func TestSearcherUsesLocalAssessmentsWhenProviderCallsAreDisabled(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{results: []Evidence{{
		EvidenceID: "evidence-1",
		JobID:      "job-1",
		BookID:     "book-1",
		Passage:    "Use local evidence without provider calls.",
		Score:      0.91,
	}}}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 0)
	assessor := &stubEvidenceAssessor{}
	searcher.SetEvidenceAssessor(assessor)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Summary != "Use local evidence without provider calls." {
		t.Fatalf("local-only result = %#v", result.Evidence)
	}
	if assessor.calls() != 0 {
		t.Fatalf("assessor calls = %d, want 0", assessor.calls())
	}
}

func TestSearcherFallsBackToLocalAssessmentAfterProviderBudgetIsExhausted(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{results: []Evidence{
		{EvidenceID: "irrelevant", JobID: "job-1", BookID: "book-1", Passage: "irrelevant evidence", Score: 0.91},
		{EvidenceID: "local", JobID: "job-2", BookID: "book-2", Passage: "keep this locally assessed evidence", Score: 0.90},
	}}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 1)
	assessor := &stubEvidenceAssessor{response: func(SummaryRequest) EvidenceAssessment {
		return EvidenceAssessment{Relevant: false}
	}}
	searcher.SetEvidenceAssessor(assessor)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "local" || result.Evidence[0].Summary != "keep this locally assessed evidence" {
		t.Fatalf("result after provider budget exhaustion = %#v", result.Evidence)
	}
	if assessor.calls() != 1 {
		t.Fatalf("assessor calls = %d, want 1", assessor.calls())
	}
}

func TestSearcherDoesNotFallbackWhenParentContextIsCanceled(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{results: []Evidence{{
		EvidenceID: "evidence-1",
		JobID:      "job-1",
		BookID:     "book-1",
		Passage:    "Do not return canceled provider work.",
		Score:      0.91,
	}}}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 1)
	searcher.SetEvidenceAssessor(&stubEvidenceAssessor{err: context.Canceled})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := searcher.Search(ctx, domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 0 || len(result.Documents) != 0 {
		t.Fatalf("canceled parent context must not use a local fallback: %#v", result)
	}
}

func TestSearchAssessmentCacheDoesNotOpenCircuitForCanceledParentContext(t *testing.T) {
	cache := newSearchAssessmentCache(2, 0, testSearchPolicy(2).MaximumAssessmentInputRunes)
	assessor := &stubEvidenceAssessor{err: context.Canceled}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if assessment, ok := cache.assess(ctx, assessor, SummaryRequest{Question: "replication", Passage: "canceled passage"}); ok || assessment != (EvidenceAssessment{}) {
		t.Fatalf("canceled assessment = %#v, %t", assessment, ok)
	}

	assessor.err = nil
	assessment, ok := cache.assess(context.Background(), assessor, SummaryRequest{Question: "replication", Passage: "fresh passage"})
	if !ok || !assessment.Relevant || assessment.Summary != "fresh passage" {
		t.Fatalf("assessment after canceled parent context = %#v, %t", assessment, ok)
	}
	if assessor.calls() != 1 {
		t.Fatalf("assessor calls = %d, want 1", assessor.calls())
	}
}

func TestSearcherWithPolicyUsesConfiguredCandidatePageMultiplier(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		resultsByPage: [][]Evidence{
			{
				{EvidenceID: "irrelevant-1", JobID: "job-1", BookID: "book-1", Passage: "off topic one", Score: 0.99},
				{EvidenceID: "irrelevant-2", JobID: "job-2", BookID: "book-2", Passage: "off topic two", Score: 0.98},
				{EvidenceID: "relevant-1", JobID: "job-3", BookID: "book-3", Passage: "relevant one", Score: 0.80},
			},
		},
	}
	assessor := &stubEvidenceAssessor{response: func(value SummaryRequest) EvidenceAssessment {
		if strings.HasPrefix(value.Passage, "relevant ") {
			return EvidenceAssessment{Relevant: true, Summary: value.Passage}
		}
		return EvidenceAssessment{Relevant: false}
	}}
	searcher, err := NewSearcherWithPolicy(embedder, store, visibleIndexes{}, assessor, SearchPolicy{
		MinimumVisibleScore:         0.6,
		AssessmentCallLimit:         4,
		CandidatePageMultiplier:     3,
		ReciprocalRankFusionK:       60,
		MaximumAssessmentInputRunes: testSearchPolicy(4).MaximumAssessmentInputRunes,
		RequestPolicy:               testSearchPolicy(4).RequestPolicy,
	})
	if err != nil {
		t.Fatalf("NewSearcherWithPolicy() error = %v", err)
	}

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 1})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "relevant-1" {
		t.Fatalf("unexpected result = %#v", result.Evidence)
	}
	if len(store.documentRequests) != 1 || store.documentRequests[0].limit != 1 || store.documentRequests[0].offset != 0 {
		t.Fatalf("unexpected document paging requests: %#v", store.documentRequests)
	}
	if len(store.requests) != 2 || store.requests[0].limit != 3 || store.requests[0].offset != 0 || store.requests[1].limit != 3 || store.requests[1].offset != 3 {
		t.Fatalf("unexpected chunk paging requests: %#v", store.requests)
	}
}

func TestSearcherExcludesIrrelevantProviderAssessmentsAndBackfills(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	results := make([]Evidence, 0, 5)
	for index := 0; index < 5; index++ {
		evidenceID := strings.Join([]string{"evidence", string(rune('a' + index))}, "-")
		passage := strings.Join([]string{"evidence", string(rune('a' + index)), "passage"}, " ")
		results = append(results, Evidence{
			EvidenceID: evidenceID,
			JobID:      "job-" + evidenceID,
			BookID:     "book-" + evidenceID,
			Title:      "Systems",
			Passage:    passage,
			Score:      0.91,
		})
	}
	store := &stubEvidenceStore{results: results}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 5)
	assessor := &stubEvidenceAssessor{response: func(value SummaryRequest) EvidenceAssessment {
		if strings.Contains(value.Passage, "evidence a") || strings.Contains(value.Passage, "evidence c") {
			return EvidenceAssessment{Relevant: false}
		}
		return EvidenceAssessment{Relevant: true, Summary: "summary: " + strings.TrimSpace(value.Passage)}
	}}
	searcher.SetEvidenceAssessor(assessor)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 3 {
		t.Fatalf("accepted evidence count = %d, want 3", len(result.Evidence))
	}
	got := map[string]struct{}{}
	for _, value := range result.Evidence {
		got[value.EvidenceID] = struct{}{}
	}
	for _, expected := range []string{"evidence-b", "evidence-d", "evidence-e"} {
		if _, found := got[expected]; !found {
			t.Fatalf("accepted evidence missing %q: %#v", expected, result.Evidence)
		}
	}
	if len(result.Evidence) != 3 || len(result.Documents) != 3 {
		t.Fatalf("irrelevant evidence should be excluded from results and document groups: %#v", result)
	}
	if assessor.calls() != 5 {
		t.Fatalf("assessor calls = %d, want 5", assessor.calls())
	}
}

func TestSearcherBackfillsAfterProviderExclusions(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		resultsByPage: [][]Evidence{
			{
				{EvidenceID: "irrelevant-1", JobID: "job-1", BookID: "book-1", Passage: "off topic one", Score: 0.99},
				{EvidenceID: "irrelevant-2", JobID: "job-2", BookID: "book-2", Passage: "off topic two", Score: 0.98},
			},
			{
				{EvidenceID: "relevant-1", JobID: "job-3", BookID: "book-3", Passage: "relevant one", Score: 0.80},
				{EvidenceID: "relevant-2", JobID: "job-4", BookID: "book-4", Passage: "relevant two", Score: 0.79},
			},
		},
	}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 4)
	assessor := &stubEvidenceAssessor{response: func(value SummaryRequest) EvidenceAssessment {
		if strings.HasPrefix(value.Passage, "relevant ") {
			return EvidenceAssessment{Relevant: true, Summary: value.Passage}
		}
		return EvidenceAssessment{Relevant: false}
	}}
	searcher.SetEvidenceAssessor(assessor)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 1})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "relevant-1" {
		t.Fatalf("unexpected backfilled evidence: %#v", result.Evidence)
	}
	if store.calls != 2 || store.documentCalls != 1 {
		t.Fatalf("evidence/doc pages = %d/%d, want 2/1", store.calls, store.documentCalls)
	}
}

func TestSearcherFiltersLowScoringEvidenceBeforeReturningResults(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		resultsByPage: [][]Evidence{
			{
				{EvidenceID: "low-1", JobID: "job-low-1", BookID: "book-low", Passage: "low evidence", Score: 0.49},
				{EvidenceID: "low-2", JobID: "job-low-2", BookID: "book-low", Passage: "low evidence", Score: 0.58},
			},
			{
				{EvidenceID: "high-1", JobID: "job-high-1", BookID: "book-high", Passage: "high evidence", Score: 0.60},
			},
		},
		documentsByPage: [][]DocumentResult{
			{
				{DocumentID: "doc-low", JobID: "job-low-1", BookID: "book-low", ChunkCount: 1, Score: 0.93, Evidence: []Evidence{{EvidenceID: "low-1", Passage: "low evidence", Score: 0.49}, {EvidenceID: "low-2", Passage: "low evidence", Score: 0.58}}},
			},
			{
				{DocumentID: "doc-high", JobID: "job-high-1", BookID: "book-high", ChunkCount: 1, Score: 0.91, Evidence: []Evidence{{EvidenceID: "high-1", Passage: "high evidence", Score: 0.60}}},
			},
		},
	}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 4)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 1})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "high-1" || result.Evidence[0].Score < 0.6 {
		t.Fatalf("low-scoring evidence was not filtered: %#v", result.Evidence)
	}
	if len(result.Documents) != 1 || result.Documents[0].DocumentID != "book-high:job-high-1" || len(result.Documents[0].Evidence) != 1 || result.Documents[0].Evidence[0].EvidenceID != "high-1" {
		t.Fatalf("low-scoring document evidence was not filtered: %#v", result.Documents)
	}
}

func TestSearcherBuildsDocumentGroupsOnlyFromAcceptedEvidence(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		results: []Evidence{
			{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Passage: "visible passage", Score: 0.81},
		},
		documents: []DocumentResult{
			{DocumentID: "book-1:job-1", JobID: "job-1", BookID: "book-1", ChunkCount: 42, Score: 0.46, Evidence: []Evidence{{EvidenceID: "evidence-1", Passage: "visible passage", Score: 0.81}}},
			{DocumentID: "document-high", JobID: "job-2", BookID: "book-2", ChunkCount: 1, Score: 0.67, Evidence: []Evidence{{EvidenceID: "evidence-2", Passage: "better visible passage", Score: 0.78}}},
		},
	}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 4)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Documents) != 1 || result.Documents[0].DocumentID != "book-1:job-1" || result.Documents[0].Evidence[0].EvidenceID != "evidence-1" || result.Documents[0].ChunkCount != 42 {
		t.Fatalf("documents = %#v", result.Documents)
	}
	if store.documentCalls != 1 {
		t.Fatalf("document search calls = %d, want 1", store.documentCalls)
	}
}

func TestSearcherUsesDocumentCandidatesWhenChunkCandidatesMiss(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		results: nil,
		documents: []DocumentResult{{
			DocumentID: "book-1:job-1",
			JobID:      "job-1",
			BookID:     "book-1",
			Title:      "Systems",
			ChunkCount: 4,
			Score:      0.87,
			Evidence: []Evidence{{
				EvidenceID: "document-evidence-1",
				JobID:      "job-1",
				BookID:     "book-1",
				Title:      "Systems",
				Passage:    "Document recall found the relevant chunk.",
				Score:      0.83,
			}},
		}},
	}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 4)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{
		Question: "document recall",
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "document-evidence-1" {
		t.Fatalf("document candidate was not returned as evidence: %#v", result.Evidence)
	}
	if len(result.Documents) != 1 || result.Documents[0].DocumentID != "book-1:job-1" || result.Documents[0].ChunkCount != 4 {
		t.Fatalf("document metadata was not preserved: %#v", result.Documents)
	}
	if store.calls != 1 || store.documentCalls != 1 {
		t.Fatalf("retrieval calls evidence/documents = %d/%d, want 1/1", store.calls, store.documentCalls)
	}
}

func TestSearcherUsesLexicalCandidatesWhenDenseCandidatesMiss(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{}
	lexicalStore := &stubLexicalEvidenceStore{
		results: []Evidence{{
			EvidenceID: "lexical-evidence-1",
			JobID:      "job-1",
			BookID:     "book-1",
			Title:      "Systems",
			Passage:    "Exact protocol code NX-42 appears only in lexical evidence.",
			Score:      0.12,
		}},
	}
	searcher := newTestSearcherWithLexical(t, embedder, store, lexicalStore, visibleIndexes{}, 4)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{
		Question: "NX-42",
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "lexical-evidence-1" {
		t.Fatalf("lexical candidate was not returned as evidence: %#v", result.Evidence)
	}
	if len(result.Documents) != 1 || result.Documents[0].DocumentID != "book-1:job-1" {
		t.Fatalf("lexical evidence was not grouped into a document: %#v", result.Documents)
	}
	if store.calls != 1 || store.documentCalls != 1 || lexicalStore.calls != 1 {
		t.Fatalf("retrieval calls dense/document/lexical = %d/%d/%d, want 1/1/1", store.calls, store.documentCalls, lexicalStore.calls)
	}
}

func TestSearcherBackfillsLexicalCandidatesAfterVisibilityFiltering(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{}
	lexicalStore := &stubLexicalEvidenceStore{
		resultsByPage: [][]Evidence{
			{
				{EvidenceID: "hidden-1", JobID: "pending-1", BookID: "book-hidden", Passage: "hidden lexical one", Score: 0.30},
				{EvidenceID: "hidden-2", JobID: "pending-2", BookID: "book-hidden", Passage: "hidden lexical two", Score: 0.29},
			},
			{
				{EvidenceID: "visible-1", JobID: "indexed-1", BookID: "book-1", Passage: "visible lexical evidence", Score: 0.20},
			},
		},
	}
	visibility := filteringVisibility{indexedJobs: map[string]struct{}{"indexed-1": {}}}
	searcher := newTestSearcherWithLexical(t, embedder, store, lexicalStore, visibility, 4)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{
		Question: "exact code",
		Limit:    1,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "visible-1" {
		t.Fatalf("unexpected lexical visible results: %#v", result.Evidence)
	}
	if lexicalStore.calls != 2 || len(lexicalStore.requests) != 2 || lexicalStore.requests[0].limit != 2 || lexicalStore.requests[0].offset != 0 || lexicalStore.requests[1].limit != 2 || lexicalStore.requests[1].offset != 2 {
		t.Fatalf("unexpected lexical paging requests: calls=%d requests=%#v", lexicalStore.calls, lexicalStore.requests)
	}
}

func TestSearcherUsesAuthoritativeDocumentChunkCountFromSearchDocuments(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		results: []Evidence{
			{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Passage: "indexed chunk one", Score: 0.91},
		},
		documents: []DocumentResult{
			{DocumentID: "book-1:job-1", JobID: "job-1", BookID: "book-1", ChunkCount: 42, Score: 0.91},
		},
	}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 4)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Documents) != 1 || result.Documents[0].ChunkCount != 42 {
		t.Fatalf("document chunk_count = %#v", result.Documents)
	}
	if store.documentCalls != 1 {
		t.Fatalf("document search calls = %d, want 1", store.documentCalls)
	}
}

func TestSearcherKeepsFusedEvidenceOrderAndSortsDocumentGroupsByScore(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		results: []Evidence{
			{EvidenceID: "evidence-low", JobID: "job-1", BookID: "book-1", Passage: "low", Score: 0.61},
			{EvidenceID: "evidence-high", JobID: "job-2", BookID: "book-2", Passage: "high", Score: 0.88},
			{EvidenceID: "evidence-mid", JobID: "job-3", BookID: "book-3", Passage: "mid", Score: 0.72},
		},
		documents: []DocumentResult{
			{
				DocumentID: "document-low", JobID: "job-1", BookID: "book-1", ChunkCount: 2, Score: 0.65,
				Evidence: []Evidence{
					{EvidenceID: "support-low", Passage: "support low", Score: 0.62},
					{EvidenceID: "support-high", Passage: "support high", Score: 0.90},
				},
			},
			{
				DocumentID: "document-high", JobID: "job-2", BookID: "book-2", ChunkCount: 1, Score: 0.93,
				Evidence: []Evidence{{EvidenceID: "support-mid", Passage: "support mid", Score: 0.77}},
			},
		},
	}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 4)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) < 3 {
		t.Fatalf("evidence ordering = %#v", result.Evidence)
	}
	if got := []string{result.Evidence[0].EvidenceID, result.Evidence[1].EvidenceID, result.Evidence[2].EvidenceID}; got[0] != "evidence-high" || got[1] != "evidence-mid" || got[2] != "evidence-low" {
		t.Fatalf("evidence ordering = %#v", got)
	}
	if len(result.Documents) != 3 {
		t.Fatalf("document ordering = %#v", result.Documents)
	}
	if got := []string{result.Documents[0].DocumentID, result.Documents[1].DocumentID, result.Documents[2].DocumentID}; got[0] != "book-2:job-2" || got[1] != "book-3:job-3" || got[2] != "book-1:job-1" {
		t.Fatalf("document ordering = %#v", got)
	}
}

func TestReciprocalRankFusionUsesConfiguredRankConstant(t *testing.T) {
	query, err := domain.NewSearchQuery(domain.SearchQueryInput{Question: "replication", Limit: 5}, testSearchPolicy(4).RequestPolicy)
	if err != nil {
		t.Fatal(err)
	}
	fusion := reciprocalRankFusion{k: 1}
	results := fusion.Fuse(query, []Evidence{
		{EvidenceID: "chunk-only", Passage: "chunk only", Score: 0.99},
		{EvidenceID: "both", Passage: "chunk and document", Score: 0.50},
	}, []DocumentResult{{
		DocumentID: "doc-1",
		Evidence: []Evidence{
			{EvidenceID: "both", Passage: "document support", Score: 0.51},
		},
	}}, []Evidence{
		{EvidenceID: "lexical-only", Passage: "lexical support", Score: 0.10},
		{EvidenceID: "both", Passage: "lexical support for both", Score: 0.49},
	})

	if len(results) != 3 || results[0].EvidenceID != "both" || results[0].Passage != "document support" {
		t.Fatalf("unexpected RRF results: %#v", results)
	}
}

func TestSearcherSkipsEmptyPassagesForAssessments(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		results: []Evidence{{EvidenceID: "evidence-1", JobID: "job-1", BookID: "book-1", Passage: "   ", Score: 0.91}},
		documents: []DocumentResult{{
			DocumentID: "document-1", JobID: "job-1", BookID: "book-1", ChunkCount: 1, Score: 0.91,
			Evidence: []Evidence{{EvidenceID: "evidence-1", Passage: "   ", Score: 0.91}},
		}},
	}
	searcher := newTestSearcher(t, embedder, store, visibleIndexes{}, 4)
	assessor := &stubEvidenceAssessor{}
	searcher.SetEvidenceAssessor(assessor)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: " replication ", Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if assessor.calls() != 0 {
		t.Fatalf("assessor calls = %d, want 0", assessor.calls())
	}
	if len(result.Evidence) != 0 || len(result.Documents) != 0 {
		t.Fatalf("empty passages should not produce results: %#v", result)
	}
}

func TestSearcherBackfillsAfterVisibilityFiltering(t *testing.T) {
	embedder := &stubEmbedder{vector: make([]float32, domain.EmbeddingDimensions)}
	store := &stubEvidenceStore{
		resultsByPage: [][]Evidence{
			{
				{EvidenceID: "pending-1", JobID: "pending-1", BookID: "book-pending", Passage: "not visible", Score: 0.59},
				{EvidenceID: "pending-2", JobID: "pending-2", BookID: "book-pending", Passage: "not visible", Score: 0.58},
			},
			{
				{EvidenceID: "visible-1", JobID: "indexed-1", BookID: "book-1", Passage: "visible one", Score: 0.80},
				{EvidenceID: "visible-2", JobID: "indexed-2", BookID: "book-2", Passage: "visible two", Score: 0.79},
			},
		},
		documentsByPage: [][]DocumentResult{
			{
				{DocumentID: "pending-document-1", JobID: "pending-1", BookID: "book-pending", ChunkCount: 1, Score: 0.99, Evidence: []Evidence{{EvidenceID: "pending-1", Score: 0.99}}},
				{DocumentID: "pending-document-2", JobID: "pending-2", BookID: "book-pending", ChunkCount: 1, Score: 0.98, Evidence: []Evidence{{EvidenceID: "pending-2", Score: 0.98}}},
			},
			{
				{DocumentID: "visible-document-1", JobID: "indexed-1", BookID: "book-1", ChunkCount: 1, Score: 0.80, Evidence: []Evidence{{EvidenceID: "visible-1", Passage: "visible one", Score: 0.80}}},
				{DocumentID: "visible-document-2", JobID: "indexed-2", BookID: "book-2", ChunkCount: 1, Score: 0.79, Evidence: []Evidence{{EvidenceID: "visible-2", Passage: "visible two", Score: 0.79}}},
			},
		},
	}
	visibility := filteringVisibility{indexedJobs: map[string]struct{}{"indexed-1": {}, "indexed-2": {}}}
	searcher := newTestSearcher(t, embedder, store, visibility, 4)

	result, err := searcher.Search(context.Background(), domain.Actor{UserID: "user-1", Role: "reader", Status: "active"}, domain.SearchQueryInput{Question: "replication", Limit: 1})

	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != "visible-1" || len(result.Documents) != 1 || result.Documents[0].DocumentID != "book-1:indexed-1" {
		t.Fatalf("unexpected visible results: %#v", result)
	}
	if store.calls != 2 || store.documentCalls != 1 {
		t.Fatalf("search pages evidence/documents = %d/%d, want 2/1", store.calls, store.documentCalls)
	}
	if len(store.documentRequests) != 1 || store.documentRequests[0].limit != 1 || store.documentRequests[0].offset != 0 {
		t.Fatalf("unexpected document paging requests: %#v", store.documentRequests)
	}
	if len(store.requests) != 2 || store.requests[0].limit != 2 || store.requests[0].offset != 0 || store.requests[1].limit != 2 || store.requests[1].offset != 2 {
		t.Fatalf("unexpected chunk paging requests: %#v", store.requests)
	}
}

type stubEmbedder struct {
	calls  int
	vector []float32
	err    error
}

func (s *stubEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	s.calls++
	return s.vector, s.err
}

type stubEvidenceStore struct {
	mu               sync.Mutex
	calls            int
	documentCalls    int
	query            domain.SearchQuery
	results          []Evidence
	documents        []DocumentResult
	resultsByPage    [][]Evidence
	documentsByPage  [][]DocumentResult
	documentPages    []DocumentPage
	requests         []searchRequest
	documentRequests []searchRequest
	err              error
}

type searchRequest struct {
	limit  int
	offset int
}

func (s *stubEvidenceStore) Search(_ context.Context, query domain.SearchQuery, _ []float32, limit, offset int) ([]Evidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.query = query
	s.requests = append(s.requests, searchRequest{limit: limit, offset: offset})
	if len(s.resultsByPage) > 0 {
		index := offset / limit
		if index < len(s.resultsByPage) {
			return s.resultsByPage[index], s.err
		}
		return nil, s.err
	}
	return s.results, s.err
}

func (s *stubEvidenceStore) SearchDocuments(_ context.Context, query domain.SearchQuery, _ []float32, limit, offset int) (DocumentPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documentCalls++
	s.query = query
	s.documentRequests = append(s.documentRequests, searchRequest{limit: limit, offset: offset})
	if len(s.documentPages) > 0 {
		index := offset / limit
		if index < len(s.documentPages) {
			return s.documentPages[index], s.err
		}
		return DocumentPage{Exhausted: true}, s.err
	}
	if len(s.documentsByPage) > 0 {
		index := offset / limit
		if index < len(s.documentsByPage) {
			return DocumentPage{Documents: s.documentsByPage[index], Exhausted: index == len(s.documentsByPage)-1}, s.err
		}
		return DocumentPage{Exhausted: true}, s.err
	}
	return DocumentPage{Documents: s.documents, Exhausted: true}, s.err
}

type stubLexicalEvidenceStore struct {
	calls         int
	query         domain.SearchQuery
	results       []Evidence
	resultsByPage [][]Evidence
	requests      []searchRequest
	err           error
}

func (s *stubLexicalEvidenceStore) SearchLexical(_ context.Context, query domain.SearchQuery, limit, offset int) ([]Evidence, error) {
	s.calls++
	s.query = query
	s.requests = append(s.requests, searchRequest{limit: limit, offset: offset})
	if len(s.resultsByPage) > 0 {
		index := offset / limit
		if index < len(s.resultsByPage) {
			return s.resultsByPage[index], s.err
		}
		return nil, s.err
	}
	return s.results, s.err
}

type visibleIndexes struct{}

func (visibleIndexes) FilterIndexed(_ context.Context, values []Evidence) ([]Evidence, error) {
	return values, nil
}

func (visibleIndexes) FilterIndexedDocuments(_ context.Context, values []DocumentResult) ([]DocumentResult, error) {
	return values, nil
}

type filteringVisibility struct {
	indexedJobs map[string]struct{}
}

func (v filteringVisibility) FilterIndexed(_ context.Context, values []Evidence) ([]Evidence, error) {
	results := make([]Evidence, 0, len(values))
	for _, value := range values {
		if _, indexed := v.indexedJobs[value.JobID]; indexed {
			results = append(results, value)
		}
	}
	return results, nil
}

func (v filteringVisibility) FilterIndexedDocuments(_ context.Context, values []DocumentResult) ([]DocumentResult, error) {
	results := make([]DocumentResult, 0, len(values))
	for _, value := range values {
		if _, indexed := v.indexedJobs[value.JobID]; indexed {
			results = append(results, value)
		}
	}
	return results, nil
}

type stubEvidenceAssessor struct {
	mu       sync.Mutex
	requests []SummaryRequest
	response func(SummaryRequest) EvidenceAssessment
	err      error
}

func (s *stubEvidenceAssessor) Assess(_ context.Context, value SummaryRequest) (EvidenceAssessment, error) {
	s.mu.Lock()
	s.requests = append(s.requests, value)
	response := s.response
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return EvidenceAssessment{}, err
	}
	if response == nil {
		return EvidenceAssessment{Relevant: true, Summary: value.Passage}, nil
	}
	return response(value), nil
}

func (s *stubEvidenceAssessor) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}
