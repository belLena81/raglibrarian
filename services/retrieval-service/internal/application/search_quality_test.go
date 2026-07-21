package application

import (
	"context"
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

type searchQualityCase struct {
	name      string
	input     domain.SearchQueryInput
	evidence  []Evidence
	documents []DocumentResult
	expected  searchQualityExpectation
}

type searchQualityExpectation struct {
	topK                  int
	evidenceID            string
	passageContains       string
	documentID            string
	documentNeedsEvidence bool
	expectEmpty           bool
	minimumScore          float64
}

func TestSearchQualityBenchmark(t *testing.T) {
	cases := []searchQualityCase{
		{
			name:  "top-k citation and document context",
			input: domain.SearchQueryInput{Question: "How do deterministic retries remain safe?", Limit: 3},
			evidence: []Evidence{
				qualityEvidence("evidence-retry", "chunk-retry", "job-retry", "book-retry", "Deterministic output makes retries harmless because replayed work reaches the same manifest.", 0.92),
				qualityEvidence("evidence-queue", "chunk-queue", "job-queue", "book-queue", "Queue depth is monitored separately from search quality.", 0.61),
			},
			documents: []DocumentResult{
				qualityDocument("document-retry", "job-retry", "book-retry", []Evidence{
					qualityEvidence("evidence-retry", "chunk-retry", "job-retry", "book-retry", "Deterministic output makes retries harmless because replayed work reaches the same manifest.", 0.92),
				}, 0.81),
			},
			expected: searchQualityExpectation{
				topK:                  1,
				evidenceID:            "evidence-retry",
				passageContains:       "Deterministic output makes retries harmless",
				documentID:            "document-retry",
				documentNeedsEvidence: true,
				minimumScore:          0.25,
			},
		},
		{
			name:      "empty result does not fabricate evidence",
			input:     domain.SearchQueryInput{Question: "unsupported topic", Filters: domain.SearchFilters{Author: "absent author"}, Limit: 5},
			expected:  searchQualityExpectation{expectEmpty: true},
			evidence:  []Evidence{},
			documents: []DocumentResult{},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			searcher, err := NewSearcher(&qualityEmbedder{}, &qualityEvidenceStore{evidence: test.evidence, documents: test.documents}, visibleIndexes{})
			if err != nil {
				t.Fatalf("NewSearcher() error = %v", err)
			}

			result, err := searcher.Search(context.Background(), domain.Actor{UserID: "reader-1", Role: "reader", Status: "active"}, test.input)
			if err != nil {
				t.Fatalf("Search() error = %v", err)
			}

			assertSearchQuality(t, result, test.expected)
		})
	}
}

func assertSearchQuality(t *testing.T, result SearchResult, expected searchQualityExpectation) {
	t.Helper()
	if expected.expectEmpty {
		if len(result.Evidence) != 0 || len(result.Documents) != 0 {
			t.Fatalf("empty benchmark fabricated %d evidence results and %d document results", len(result.Evidence), len(result.Documents))
		}
		return
	}
	if expected.topK < 1 || expected.topK > len(result.Evidence) {
		t.Fatalf("benchmark returned %d evidence results, want at least top-%d", len(result.Evidence), expected.topK)
	}
	evidence := result.Evidence[expected.topK-1]
	if evidence.EvidenceID != expected.evidenceID || evidence.ChunkID == "" || evidence.BookID == "" ||
		evidence.PageStart < 1 || evidence.PageEnd < evidence.PageStart || evidence.Score < expected.minimumScore ||
		!strings.Contains(evidence.Passage, expected.passageContains) {
		t.Fatalf("top-%d evidence did not satisfy citation benchmark: %#v", expected.topK, evidence)
	}
	if expected.documentID == "" {
		return
	}
	for _, document := range result.Documents {
		if document.DocumentID != expected.documentID {
			continue
		}
		if document.BookID == "" || document.ChunkCount == 0 || document.PageStart < 1 || document.PageEnd < document.PageStart ||
			document.Score < expected.minimumScore || (expected.documentNeedsEvidence && len(document.Evidence) == 0) {
			t.Fatalf("document benchmark returned an invalid document result: %#v", document)
		}
		return
	}
	t.Fatalf("document benchmark did not return %q", expected.documentID)
}

type qualityEmbedder struct{}

func (*qualityEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	vector := make([]float32, domain.EmbeddingDimensions)
	vector[0] = 1
	return vector, nil
}

type qualityEvidenceStore struct {
	evidence  []Evidence
	documents []DocumentResult
}

func (s *qualityEvidenceStore) Search(_ context.Context, query domain.SearchQuery, _ []float32, limit, offset int) ([]Evidence, error) {
	if offset >= len(s.evidence) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.evidence) {
		end = len(s.evidence)
	}
	return s.evidence[offset:end], nil
}

func (s *qualityEvidenceStore) SearchDocuments(_ context.Context, query domain.SearchQuery, _ []float32, limit, offset int) (DocumentPage, error) {
	if offset >= len(s.documents) {
		return DocumentPage{Exhausted: true}, nil
	}
	end := offset + limit
	if end > len(s.documents) {
		end = len(s.documents)
	}
	return DocumentPage{Documents: s.documents[offset:end], Exhausted: end == len(s.documents)}, nil
}

func qualityEvidence(evidenceID, chunkID, jobID, bookID, passage string, score float64) Evidence {
	return Evidence{
		EvidenceID: evidenceID,
		ChunkID:    chunkID,
		JobID:      jobID,
		BookID:     bookID,
		Title:      "Synthetic Systems",
		Author:     "RAGLibrarian QA",
		Year:       2026,
		Tags:       []string{"m5-quality"},
		Chapter:    "Deterministic Retrieval",
		Section:    "Retries",
		PageStart:  10,
		PageEnd:    11,
		Passage:    passage,
		Score:      score,
	}
}

func qualityDocument(documentID, jobID, bookID string, evidence []Evidence, score float64) DocumentResult {
	return DocumentResult{
		DocumentID: documentID,
		JobID:      jobID,
		BookID:     bookID,
		Title:      "Synthetic Systems",
		Author:     "RAGLibrarian QA",
		Year:       2026,
		Tags:       []string{"m5-quality"},
		ChunkCount: 12,
		PageStart:  1,
		PageEnd:    42,
		Score:      score,
		Evidence:   evidence,
	}
}
