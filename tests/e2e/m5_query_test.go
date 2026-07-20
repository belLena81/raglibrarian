//go:build e2e && m5

package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type m5Book struct {
	ID                        string `json:"id"`
	ProcessingStatus          string `json:"processing_status"`
	ProcessingStage           string `json:"processing_stage"`
	ProcessingFailureCategory string `json:"processing_failure_category"`
}

type m5QueryResponse struct {
	Query   string `json:"query"`
	Results []struct {
		EvidenceID string `json:"evidence_id"`
		ChunkID    string `json:"chunk_id"`
		Book       struct {
			ID string `json:"id"`
		} `json:"book"`
		Pages   [2]uint32 `json:"pages"`
		Passage string    `json:"passage"`
		Score   float64   `json:"score"`
	} `json:"results"`
}

func TestM5AuthenticatedUploadIndexesAndReturnsExactEvidence(t *testing.T) {
	token := readM5SecretFile(t, "M5_E2E_LIBRARIAN_TOKEN_FILE")
	book := uploadM5Fixture(t, token, "multipage.pdf")
	waitForM5Book(t, token, book.ID, "indexed")

	result := queryM5(t, token, map[string]any{"question": "Why are deterministic retries harmless?", "limit": 5})
	if result.Query != "Why are deterministic retries harmless?" || len(result.Results) == 0 {
		t.Fatalf("semantic query returned no evidence for indexed fixture")
	}
	found := false
	for _, evidence := range result.Results {
		if evidence.Book.ID == book.ID && strings.Contains(evidence.Passage, "Deterministic output makes retries harmless") {
			if evidence.EvidenceID == "" || evidence.ChunkID == "" || evidence.Score < 0.25 || evidence.Pages[1] < evidence.Pages[0] {
				t.Fatal("matching evidence had an invalid citation contract")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("query did not return the exact indexed synthetic passage and citation")
	}

	empty := queryM5(t, token, map[string]any{"question": "deterministic retries", "filters": map[string]any{"author": "No Such Synthetic Author"}})
	if len(empty.Results) != 0 {
		t.Fatalf("unrelated metadata filter returned %d results", len(empty.Results))
	}
}

func TestM5AllActiveRolesCanQueryAndUnauthenticatedCallerCannot(t *testing.T) {
	for _, environmentKey := range []string{"M5_E2E_READER_TOKEN_FILE", "M5_E2E_LIBRARIAN_TOKEN_FILE", "M5_E2E_ADMIN_TOKEN_FILE"} {
		t.Run(environmentKey, func(t *testing.T) {
			result := queryM5(t, readM5SecretFile(t, environmentKey), map[string]any{"question": "synthetic systems", "limit": 1})
			if result.Query != "synthetic systems" {
				t.Fatal("authenticated query was not normalized and echoed")
			}
		})
	}
	response := request(t, http.MethodPost, "/query", map[string]any{"question": "replication"}, "")
	requireStatus(t, http.StatusUnauthorized, response)
	_ = requireSanitizedError(t, response)
}

func TestM5PerformanceSearchesIndexedEvidenceWithinBudget(t *testing.T) {
	token := readM5SecretFile(t, "M5_E2E_LIBRARIAN_TOKEN_FILE")
	started := time.Now()
	for index := 0; index < 20; index++ {
		result := queryM5(t, token, map[string]any{"question": "deterministic retries", "limit": 5})
		if len(result.Results) == 0 {
			t.Fatal("performance query returned no indexed evidence")
		}
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("20 authenticated semantic searches took %s", elapsed)
	}
}

func uploadM5Fixture(t *testing.T, token, name string) m5Book {
	t.Helper()
	directory := os.Getenv("M5_E2E_FIXTURE_DIR")
	if directory == "" || filepath.Base(name) != name {
		t.Fatal("M5_E2E_FIXTURE_DIR and a safe fixture name are required")
	}
	fixture, err := os.Open(filepath.Join(directory, name)) // #nosec G304 -- test-owned fixture directory and constant name.
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadataHeader := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="metadata"`}, "Content-Type": {"application/json"}}
	metadata, err := writer.CreatePart(metadataHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(metadata, `{"title":"M5 deterministic systems","author":"RAGLibrarian QA","year":2026,"tags":["m5-synthetic"]}`)
	fileHeader := textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="file"; filename=%q`, name)}, "Content-Type": {"application/pdf"}}
	part, err := writer.CreatePart(fileHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(part, io.LimitReader(fixture, (25<<20)+1)); err != nil || writer.Close() != nil {
		t.Fatal("encode synthetic upload")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL()+"/books", &body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, http.StatusCreated, response)
	var book m5Book
	decodeJSON(t, response, &book)
	if book.ID == "" {
		t.Fatal("upload returned no book identity")
	}
	return book
}

func waitForM5Book(t *testing.T, token, bookID, stage string) m5Book {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		response := request(t, http.MethodGet, "/books/"+bookID, nil, token)
		requireStatus(t, http.StatusOK, response)
		var book m5Book
		decodeJSON(t, response, &book)
		if book.ProcessingStage == stage {
			return book
		}
		if book.ProcessingStatus == "failed" {
			t.Fatalf("M5 preparation failed with category %q", book.ProcessingFailureCategory)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("book %s did not reach stage %s", bookID, stage)
	return m5Book{}
}

func queryM5(t *testing.T, token string, input map[string]any) m5QueryResponse {
	t.Helper()
	response := request(t, http.MethodPost, "/query", input, token)
	requireStatus(t, http.StatusOK, response)
	var result m5QueryResponse
	decodeJSON(t, response, &result)
	return result
}

func readM5SecretFile(t *testing.T, key string) string {
	t.Helper()
	path := os.Getenv(key)
	if path == "" {
		t.Fatalf("%s is required", key)
	}
	file, err := os.Open(path) // #nosec G304 -- runner-owned credential file.
	if err != nil {
		t.Fatalf("%s is unavailable", key)
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, 4097))
	token := strings.TrimSpace(string(value))
	if err != nil || len(value) > 4096 || token == "" {
		t.Fatalf("%s is invalid", key)
	}
	return token
}
