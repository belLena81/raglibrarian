//go:build e2e && m5 && m7

package e2e_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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

const m7EPUBMediaType = "application/epub+zip"

type m7Book struct {
	ID                        string `json:"id"`
	MediaType                 string `json:"media_type"`
	ProcessingStatus          string `json:"processing_status"`
	ProcessingStage           string `json:"processing_stage"`
	ProcessingFailureCategory string `json:"processing_failure_category"`
	LifecycleVersion          uint64 `json:"lifecycle_version"`
	CanReindex                bool   `json:"can_reindex"`
}

type m7QueryResponse struct {
	Results []m7Evidence `json:"results"`
}

type m7Evidence struct {
	Book struct {
		ID        string `json:"id"`
		MediaType string `json:"media_type"`
	} `json:"book"`
	Pages   [2]uint32 `json:"pages"`
	Passage string    `json:"passage"`
}

func TestM7EPUBReindexAndDeleteLifecycleConvergesIdempotently(t *testing.T) {
	token := readM7SecretFile(t, "M7_E2E_LIBRARIAN_TOKEN_FILE")
	book := uploadM7EPUB(t, token)
	book = waitForM7BookStage(t, token, book.ID, "indexed", book.LifecycleVersion)
	if book.MediaType != m7EPUBMediaType || !book.CanReindex {
		t.Fatalf("indexed EPUB projection has media_type %q and can_reindex %t", book.MediaType, book.CanReindex)
	}

	assertM7EPUBEvidence(t, queryM7(t, token), book.ID)

	reindexKey := randomM7IdempotencyKey(t)
	firstReindex := commandM7Book(t, token, http.MethodPost, book.ID+"/reindex", reindexKey)
	replayedReindex := commandM7Book(t, token, http.MethodPost, book.ID+"/reindex", reindexKey)
	if firstReindex.ID != book.ID || replayedReindex.ID != book.ID ||
		firstReindex.LifecycleVersion != replayedReindex.LifecycleVersion {
		t.Fatal("replayed reindex command did not return the same lifecycle projection")
	}
	if firstReindex.LifecycleVersion <= book.LifecycleVersion {
		t.Fatal("reindex command did not advance the lifecycle version")
	}
	reindexed := waitForM7BookStage(t, token, book.ID, "indexed", firstReindex.LifecycleVersion)
	if !reindexed.CanReindex || reindexed.MediaType != m7EPUBMediaType {
		t.Fatal("reindex convergence lost the validated EPUB manifest projection")
	}
	assertM7EPUBEvidence(t, queryM7(t, token), book.ID)

	deleteKey := randomM7IdempotencyKey(t)
	firstDelete := commandM7Book(t, token, http.MethodDelete, book.ID, deleteKey)
	replayedDelete := commandM7Book(t, token, http.MethodDelete, book.ID, deleteKey)
	if firstDelete.ID != book.ID || replayedDelete.ID != book.ID ||
		firstDelete.LifecycleVersion != replayedDelete.LifecycleVersion {
		t.Fatal("replayed delete command did not return the same lifecycle projection")
	}
	if firstDelete.LifecycleVersion <= reindexed.LifecycleVersion {
		t.Fatal("delete command did not advance the lifecycle version")
	}

	waitForM7CatalogDeletion(t, token, book.ID)
	waitForM7EvidenceDeletion(t, token, book.ID)
}

func uploadM7EPUB(t *testing.T, token string) m7Book {
	t.Helper()
	directory := strings.TrimSpace(os.Getenv("M7_E2E_FIXTURE_DIR"))
	const name = "locations.epub"
	if directory == "" {
		t.Fatal("M7_E2E_FIXTURE_DIR is required")
	}
	fixture, err := os.Open(filepath.Join(directory, name)) // #nosec G304 -- test-owned fixture directory and constant name.
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadataHeader := textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="metadata"`},
		"Content-Type":        {"application/json"},
	}
	metadata, err := writer.CreatePart(metadataHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(metadata, `{"title":"M7 synthetic locations","author":"RAGLibrarian QA","year":2026,"tags":["m7-synthetic"]}`)
	if err != nil {
		t.Fatal(err)
	}
	fileHeader := textproto.MIMEHeader{
		"Content-Disposition": {fmt.Sprintf(`form-data; name="file"; filename=%q`, name)},
		"Content-Type":        {m7EPUBMediaType},
	}
	part, err := writer.CreatePart(fileHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(part, io.LimitReader(fixture, (25<<20)+1)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL()+"/books", &body)
	if err != nil {
		t.Fatal(err)
	}
	addBrowserMutationHeaders(request)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, http.StatusCreated, response)
	var book m7Book
	decodeJSON(t, response, &book)
	if book.ID == "" || book.MediaType != m7EPUBMediaType {
		t.Fatalf("EPUB upload returned ID %q and media_type %q", book.ID, book.MediaType)
	}
	return book
}

func commandM7Book(t *testing.T, token, method, path, idempotencyKey string) m7Book {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, baseURL()+"/books/"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	addBrowserMutationHeaders(request)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, http.StatusAccepted, response)
	var book m7Book
	decodeJSON(t, response, &book)
	return book
}

func waitForM7BookStage(t *testing.T, token, bookID, stage string, minimumLifecycleVersion uint64) m7Book {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		response := request(t, http.MethodGet, "/books/"+bookID, nil, token)
		requireStatus(t, http.StatusOK, response)
		var book m7Book
		decodeJSON(t, response, &book)
		if book.ProcessingStatus == "failed" {
			t.Fatalf("M7 lifecycle failed with category %q", book.ProcessingFailureCategory)
		}
		if book.ProcessingStage == stage && book.LifecycleVersion >= minimumLifecycleVersion {
			return book
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("book %s did not reach stage %s at lifecycle version %d", bookID, stage, minimumLifecycleVersion)
	return m7Book{}
}

func queryM7(t *testing.T, token string) m7QueryResponse {
	t.Helper()
	response := request(t, http.MethodPost, "/query", map[string]any{
		"question": "Clockwork indexes converge after a replayed command.",
		"limit":    5,
	}, token)
	requireStatus(t, http.StatusOK, response)
	var result m7QueryResponse
	decodeJSON(t, response, &result)
	return result
}

func assertM7EPUBEvidence(t *testing.T, result m7QueryResponse, bookID string) {
	t.Helper()
	for _, evidence := range result.Results {
		if evidence.Book.ID != bookID || !strings.Contains(evidence.Passage, "Clockwork indexes converge") {
			continue
		}
		if evidence.Book.MediaType != m7EPUBMediaType {
			t.Fatalf("EPUB evidence has media_type %q", evidence.Book.MediaType)
		}
		if evidence.Pages[0] == 0 || evidence.Pages[0] > 2 || evidence.Pages[1] < 2 {
			t.Fatalf("EPUB evidence does not cite spine location 2: %v", evidence.Pages)
		}
		return
	}
	t.Fatal("query did not return the exact EPUB evidence")
}

func waitForM7CatalogDeletion(t *testing.T, token, bookID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		response := request(t, http.MethodGet, "/books/"+bookID, nil, token)
		if response.StatusCode == http.StatusNotFound {
			response.Body.Close()
			return
		}
		if response.StatusCode != http.StatusOK {
			status := response.StatusCode
			response.Body.Close()
			t.Fatalf("catalog deletion poll returned HTTP %d", status)
		}
		response.Body.Close()
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("book %s remained visible in Catalog after delete convergence", bookID)
}

func waitForM7EvidenceDeletion(t *testing.T, token, bookID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		found := false
		for _, evidence := range queryM7(t, token).Results {
			if evidence.Book.ID == bookID {
				found = true
				break
			}
		}
		if !found {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("book %s remained searchable after delete convergence", bookID)
}

func randomM7IdempotencyKey(t *testing.T) string {
	t.Helper()
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	return "m7-" + hex.EncodeToString(value[:])
}

func readM7SecretFile(t *testing.T, key string) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(key))
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
