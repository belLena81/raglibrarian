//go:build e2e && m5 && m6

package e2e_test

import (
	"net/http"
	"testing"
	"time"
)

type m6AnswerResponse struct {
	m5QueryResponse
	Answer *struct {
		Segments []struct {
			Text        string   `json:"text"`
			EvidenceIDs []string `json:"evidence_ids"`
		} `json:"segments"`
	} `json:"answer,omitempty"`
}

func TestM6SearchRemainsCompatibleAndAnswerCitesReturnedEvidence(t *testing.T) {
	librarianToken := readM5SecretFile(t, "M5_E2E_LIBRARIAN_TOKEN_FILE")
	book := uploadM5Fixture(t, librarianToken, "multipage.pdf")
	waitForM5Book(t, librarianToken, book.ID, "indexed")
	token := readM5SecretFile(t, "M5_E2E_READER_TOKEN_FILE")

	search := queryM6(t, token, map[string]any{"question": "deterministic retries", "limit": 5})
	if search.Answer != nil || len(search.Results) == 0 {
		t.Fatal("default search mode changed or returned no durable evidence")
	}

	answered := queryM6(t, token, map[string]any{"question": "deterministic retries", "mode": "answer", "limit": 5})
	if answered.Answer == nil || len(answered.Answer.Segments) == 0 {
		t.Fatal("answer mode returned no grounded answer")
	}
	evidenceIDs := make(map[string]struct{}, len(answered.Results))
	for _, evidence := range answered.Results {
		evidenceIDs[evidence.EvidenceID] = struct{}{}
	}
	for _, document := range answered.Documents {
		for _, evidence := range document.Evidence {
			evidenceIDs[evidence.EvidenceID] = struct{}{}
		}
	}
	for _, segment := range answered.Answer.Segments {
		if segment.Text == "" || len(segment.EvidenceIDs) == 0 {
			t.Fatal("answer segment is empty or uncited")
		}
		for _, evidenceID := range segment.EvidenceIDs {
			if _, exists := evidenceIDs[evidenceID]; !exists {
				t.Fatal("answer cited evidence not returned by Retrieval")
			}
		}
	}
}

func TestM6EmptyEvidenceDegradesWithoutFabrication(t *testing.T) {
	token := readM5SecretFile(t, "M5_E2E_READER_TOKEN_FILE")
	result := queryM6(t, token, map[string]any{
		"question": "deterministic retries",
		"mode":     "answer",
		"filters":  map[string]any{"author": "No Such Synthetic Author"},
	})
	if result.Answer != nil || len(result.Results) != 0 || len(result.Documents) != 0 {
		t.Fatal("empty Retrieval result fabricated an answer or evidence")
	}
}

func TestM6PerformanceAnswersWithinBudget(t *testing.T) {
	const answerRequests = 8

	token := readM5SecretFile(t, "M5_E2E_READER_TOKEN_FILE")
	// Keep this smoke inside the default EDGE_ANSWER_RATE_LIMIT=10/minute.
	// The combined M6 integration gate consumes two answer-mode requests with
	// the same reader principal before invoking this performance smoke.
	durations := make([]time.Duration, 0, answerRequests)
	for index := 0; index < answerRequests; index++ {
		started := time.Now()
		result := queryM6(t, token, map[string]any{"question": "deterministic retries", "mode": "answer", "limit": 5})
		durations = append(durations, time.Since(started))
		if result.Answer == nil {
			t.Fatal("deterministic provider degraded during performance smoke")
		}
	}
	for left := 0; left < len(durations); left++ {
		for right := left + 1; right < len(durations); right++ {
			if durations[right] < durations[left] {
				durations[left], durations[right] = durations[right], durations[left]
			}
		}
	}
	p95Index := (95*len(durations)+99)/100 - 1
	if p95 := durations[p95Index]; p95 >= 3*time.Second {
		t.Fatalf("M6 deterministic answer p95 exceeded budget: %s", p95)
	}
}

func queryM6(t *testing.T, token string, input map[string]any) m6AnswerResponse {
	t.Helper()
	response := request(t, http.MethodPost, "/query", input, token)
	requireStatus(t, http.StatusOK, response)
	var result m6AnswerResponse
	decodeJSON(t, response, &result)
	return result
}
