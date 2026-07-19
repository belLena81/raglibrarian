//go:build e2e && m4

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const m4MaxFixtureBytes = 64 << 20

type m4Environment struct {
	accessToken  string
	fixtureDir   string
	edgeURLs     []string
	publicOrigin string
	timeout      time.Duration
}

type m4Book struct {
	ID                        string    `json:"id"`
	ProcessingStatus          string    `json:"processing_status"`
	ProcessingStage           string    `json:"processing_stage"`
	ProcessingFailureCategory string    `json:"processing_failure_category"`
	ProcessingVersion         int64     `json:"processing_version"`
	ProcessingUpdatedAt       time.Time `json:"processing_updated_at"`
}

type m4SSEEvent struct {
	id        string
	eventType string
	data      []byte
}

type m4EventStream struct {
	body   io.ReadCloser
	reader *bufio.Reader
}

type m4SSEPayload struct {
	BookID                    string    `json:"book_id"`
	ProcessingStatus          string    `json:"processing_status"`
	ProcessingStage           string    `json:"processing_stage"`
	ProcessingFailureCategory string    `json:"processing_failure_category"`
	ProcessingVersion         int64     `json:"processing_version"`
	UpdatedAt                 time.Time `json:"updated_at"`
	SchemaVersion             int       `json:"schema_version"`
}

func TestM4SyntheticPDFCorpusReachesDeterministicStatus(t *testing.T) {
	environment := loadM4Environment(t, false)
	tests := []struct {
		fixture     string
		wantStatus  string
		wantStage   string
		wantFailure string
	}{
		{fixture: "minimal.pdf", wantStatus: "processing", wantStage: "chunks_ready"},
		{fixture: "multipage.pdf", wantStatus: "processing", wantStage: "chunks_ready"},
		{fixture: "blank_middle_page.pdf", wantStatus: "processing", wantStage: "chunks_ready"},
		{fixture: "image_only.pdf", wantStatus: "failed", wantStage: "failed", wantFailure: "no_extractable_text"},
		{fixture: "encrypted.pdf", wantStatus: "failed", wantStage: "failed", wantFailure: "encrypted_document"},
		{fixture: "malformed.pdf", wantStatus: "failed", wantStage: "failed", wantFailure: "malformed_document"},
	}

	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			book := uploadM4Fixture(t, environment.edgeURLs[0], environment.accessToken, environment.fixtureDir, test.fixture)
			book = waitForM4Status(t, environment, environment.edgeURLs[0], book.ID, func(current m4Book) bool {
				return current.ProcessingStatus == test.wantStatus && current.ProcessingStage == test.wantStage
			})

			assert.Equal(t, test.wantFailure, book.ProcessingFailureCategory)
			assert.Positive(t, book.ProcessingVersion)
			assert.False(t, book.ProcessingUpdatedAt.IsZero())
		})
	}
}

func TestM4SSERequiresBearerAuthenticationAndStartsWithResync(t *testing.T) {
	environment := loadM4Environment(t, false)

	request, err := http.NewRequest(http.MethodGet, environment.edgeURLs[0]+"/books/events", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", environment.publicOrigin)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := openM4EventStream(t, ctx, environment.edgeURLs[0], environment.publicOrigin, environment.accessToken, "")
	event, err := readM4SSEEvent(stream)
	require.NoError(t, err)
	assert.Equal(t, "books-resync", event.eventType)
}

func TestM4SSEClosesAfterSessionRevocation(t *testing.T) {
	environment := loadM4Environment(t, false)
	revocableToken := strings.TrimSpace(os.Getenv("M4_E2E_REVOCABLE_ACCESS_TOKEN"))
	if revocableToken == "" {
		t.Fatal("M4_E2E_REVOCABLE_ACCESS_TOKEN is required and must belong to a disposable active session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	stream := openM4EventStream(t, ctx, environment.edgeURLs[0], environment.publicOrigin, revocableToken, "")
	event, err := readM4SSEEvent(stream)
	require.NoError(t, err)
	require.Equal(t, "books-resync", event.eventType)

	logoutRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, environment.edgeURLs[0]+"/auth/logout", nil)
	require.NoError(t, err)
	logoutRequest.Header.Set("Authorization", "Bearer "+revocableToken)
	logoutRequest.Header.Set("Origin", environment.publicOrigin)
	logoutResponse, err := http.DefaultClient.Do(logoutRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, logoutResponse.StatusCode)
	require.NoError(t, logoutResponse.Body.Close())

	closed := make(chan error, 1)
	go func() {
		for {
			if _, readErr := readM4SSEEvent(stream); readErr != nil {
				closed <- readErr
				return
			}
		}
	}()
	select {
	case <-closed:
	case <-time.After(20 * time.Second):
		t.Fatal("SSE stream remained open beyond the session revalidation window")
	}

	rejectedRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, environment.edgeURLs[0]+"/books/events", nil)
	require.NoError(t, err)
	rejectedRequest.Header.Set("Accept", "text/event-stream")
	rejectedRequest.Header.Set("Authorization", "Bearer "+revocableToken)
	rejectedRequest.Header.Set("Origin", environment.publicOrigin)
	rejectedResponse, err := http.DefaultClient.Do(rejectedRequest)
	require.NoError(t, err)
	defer rejectedResponse.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, rejectedResponse.StatusCode)
}

func TestM4StatusEventsFanOutAndReconcileAcrossEdgeInstances(t *testing.T) {
	environment := loadM4Environment(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), environment.timeout)
	defer cancel()

	streams := make([]*m4EventStream, 0, len(environment.edgeURLs))
	for _, edgeURL := range environment.edgeURLs {
		streams = append(streams, openM4EventStream(t, ctx, edgeURL, environment.publicOrigin, environment.accessToken, ""))
	}
	book := uploadM4Fixture(t, environment.edgeURLs[0], environment.accessToken, environment.fixtureDir, "minimal.pdf")

	for index, stream := range streams {
		payload, eventID := waitForM4ReadyEvent(t, stream, book.ID)
		assert.NotEmpty(t, eventID, "Edge instance %d omitted the Catalog event ID", index)
		assert.Equal(t, "processing", payload.ProcessingStatus)
		assert.Equal(t, "chunks_ready", payload.ProcessingStage)
		assert.Empty(t, payload.ProcessingFailureCategory)
		assert.Positive(t, payload.ProcessingVersion)
		assert.Equal(t, 1, payload.SchemaVersion)
		assert.False(t, payload.UpdatedAt.IsZero())
	}

	var authoritative m4Book
	for _, edgeURL := range environment.edgeURLs {
		current := getM4Book(t, edgeURL, environment.accessToken, book.ID)
		if authoritative.ID == "" {
			authoritative = current
			continue
		}
		assert.Equal(t, authoritative.ProcessingStatus, current.ProcessingStatus)
		assert.Equal(t, authoritative.ProcessingStage, current.ProcessingStage)
		assert.Equal(t, authoritative.ProcessingVersion, current.ProcessingVersion)
	}
	for _, stream := range streams {
		require.NoError(t, stream.body.Close())
	}

	reconnectCtx, reconnectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer reconnectCancel()
	reconnected := openM4EventStream(t, reconnectCtx, environment.edgeURLs[0], environment.publicOrigin, environment.accessToken, "missed-event")
	event, err := readM4SSEEvent(reconnected)
	require.NoError(t, err)
	assert.Equal(t, "books-resync", event.eventType, "missed events must trigger authoritative reconciliation")
	reconciled := getM4Book(t, environment.edgeURLs[0], environment.accessToken, book.ID)
	assert.Equal(t, authoritative.ProcessingVersion, reconciled.ProcessingVersion)
}

func loadM4Environment(t *testing.T, requireFanout bool) m4Environment {
	t.Helper()
	environment := m4Environment{
		accessToken:  strings.TrimSpace(os.Getenv("M4_E2E_ACCESS_TOKEN")),
		fixtureDir:   strings.TrimSpace(os.Getenv("M4_E2E_FIXTURE_DIR")),
		publicOrigin: strings.TrimRight(strings.TrimSpace(os.Getenv("M4_E2E_PUBLIC_ORIGIN")), "/"),
		timeout:      2 * time.Minute,
	}
	if environment.accessToken == "" {
		t.Fatal("M4_E2E_ACCESS_TOKEN is required")
	}
	if environment.fixtureDir == "" {
		t.Fatal("M4_E2E_FIXTURE_DIR is required; generate it with tests/fixtures/ingestion/generate.go")
	}
	origin, err := url.Parse(environment.publicOrigin)
	if environment.publicOrigin == "" || err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		t.Fatal("M4_E2E_PUBLIC_ORIGIN must be an absolute HTTP(S) origin")
	}
	if value := strings.TrimSpace(os.Getenv("M4_E2E_STATUS_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Second || parsed > 15*time.Minute {
			t.Fatal("M4_E2E_STATUS_TIMEOUT must be between 1s and 15m")
		}
		environment.timeout = parsed
	}
	for _, value := range strings.Split(os.Getenv("M4_E2E_EDGE_BASE_URLS"), ",") {
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Fatalf("M4_E2E_EDGE_BASE_URLS contains an invalid base URL")
		}
		environment.edgeURLs = append(environment.edgeURLs, value)
	}
	if len(environment.edgeURLs) == 0 {
		t.Fatal("M4_E2E_EDGE_BASE_URLS is required")
	}
	if requireFanout && len(environment.edgeURLs) < 2 {
		t.Fatal("M4_E2E_EDGE_BASE_URLS must contain at least two Edge instances for the fanout test")
	}
	return environment
}

func uploadM4Fixture(t *testing.T, edgeURL, token, fixtureDir, fixtureName string) m4Book {
	t.Helper()
	fixture, err := os.Open(filepath.Join(fixtureDir, fixtureName)) // #nosec G304 -- fixtureName is a test-owned constant.
	require.NoError(t, err)
	defer fixture.Close()
	info, err := fixture.Stat()
	require.NoError(t, err)
	require.Positive(t, info.Size())
	require.LessOrEqual(t, info.Size(), int64(m4MaxFixtureBytes))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadataHeader := textproto.MIMEHeader{}
	metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metadataHeader.Set("Content-Type", "application/json")
	metadata, err := writer.CreatePart(metadataHeader)
	require.NoError(t, err)
	_, err = fmt.Fprintf(metadata, `{"title":"M4 synthetic %s","author":"RAGLibrarian QA","year":2026,"tags":["m4-synthetic"]}`, fixtureName)
	require.NoError(t, err)
	fileHeader := textproto.MIMEHeader{}
	fileHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%s`, strconv.Quote(fixtureName)))
	fileHeader.Set("Content-Type", "application/pdf")
	file, err := writer.CreatePart(fileHeader)
	require.NoError(t, err)
	_, err = io.Copy(file, io.LimitReader(fixture, m4MaxFixtureBytes+1))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, edgeURL+"/books", &body)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", loadM4PublicOrigin(t))
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	var book m4Book
	decodeJSON(t, response, &book)
	require.NotEmpty(t, book.ID)
	return book
}

func waitForM4Status(t *testing.T, environment m4Environment, edgeURL, bookID string, done func(m4Book) bool) m4Book {
	t.Helper()
	deadline := time.Now().Add(environment.timeout)
	var last m4Book
	for time.Now().Before(deadline) {
		last = getM4Book(t, edgeURL, environment.accessToken, bookID)
		if done(last) {
			return last
		}
		if last.ProcessingStatus == "failed" || last.ProcessingStage == "chunks_ready" {
			t.Fatalf("book reached unexpected terminal M4 projection: status=%q stage=%q category=%q", last.ProcessingStatus, last.ProcessingStage, last.ProcessingFailureCategory)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("book did not reach expected M4 projection; last status=%q stage=%q category=%q version=%d", last.ProcessingStatus, last.ProcessingStage, last.ProcessingFailureCategory, last.ProcessingVersion)
	return m4Book{}
}

func getM4Book(t *testing.T, edgeURL, token, bookID string) m4Book {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, edgeURL+"/books/"+url.PathEscape(bookID), nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	var book m4Book
	decodeJSON(t, response, &book)
	return book
}

func openM4EventStream(t *testing.T, ctx context.Context, edgeURL, publicOrigin, token, lastEventID string) *m4EventStream {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, edgeURL+"/books/events", nil)
	require.NoError(t, err)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Origin", publicOrigin)
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "text/event-stream", strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	cacheControl := strings.ToLower(response.Header.Get("Cache-Control"))
	require.Contains(t, cacheControl, "no-store")
	require.Contains(t, cacheControl, "private")
	t.Cleanup(func() { _ = response.Body.Close() })
	return &m4EventStream{body: response.Body, reader: bufio.NewReaderSize(response.Body, 64<<10)}
}

func loadM4PublicOrigin(t *testing.T) string {
	t.Helper()
	origin := strings.TrimRight(strings.TrimSpace(os.Getenv("M4_E2E_PUBLIC_ORIGIN")), "/")
	if origin == "" {
		t.Fatal("M4_E2E_PUBLIC_ORIGIN is required")
	}
	return origin
}

func readM4SSEEvent(stream *m4EventStream) (m4SSEEvent, error) {
	var event m4SSEEvent
	for {
		rawLine, err := stream.reader.ReadSlice('\n')
		if err != nil && len(rawLine) == 0 {
			return m4SSEEvent{}, err
		}
		if err == bufio.ErrBufferFull {
			return m4SSEEvent{}, fmt.Errorf("SSE line exceeds 64 KiB")
		}
		line := strings.TrimSuffix(strings.TrimSuffix(string(rawLine), "\n"), "\r")
		if line == "" {
			if event.eventType != "" || len(event.data) > 0 {
				return event, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "id":
			event.id = value
		case "event":
			event.eventType = value
		case "data":
			if len(event.data) > 0 {
				event.data = append(event.data, '\n')
			}
			event.data = append(event.data, value...)
		}
		if err == io.EOF {
			if event.eventType != "" || len(event.data) > 0 {
				return event, nil
			}
			return m4SSEEvent{}, io.EOF
		}
	}
}

func waitForM4ReadyEvent(t *testing.T, stream *m4EventStream, bookID string) (m4SSEPayload, string) {
	t.Helper()
	for {
		event, err := readM4SSEEvent(stream)
		require.NoError(t, err)
		if event.eventType == "books-resync" {
			continue
		}
		require.Equal(t, "book-processing-status-changed", event.eventType)
		require.NotEmpty(t, event.id)
		require.LessOrEqual(t, len(event.id), 128)
		require.NotContains(t, event.id, "\r")
		require.NotContains(t, event.id, "\n")
		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(event.data, &raw))
		allowed := map[string]bool{
			"book_id": true, "processing_status": true, "processing_stage": true,
			"processing_failure_category": true, "processing_version": true,
			"updated_at": true, "schema_version": true,
		}
		for key := range raw {
			assert.True(t, allowed[key], "SSE payload exposed unexpected field %q", key)
		}
		for _, key := range []string{"book_id", "processing_status", "processing_stage", "processing_version", "updated_at", "schema_version"} {
			assert.Contains(t, raw, key)
		}
		var payload m4SSEPayload
		require.NoError(t, json.Unmarshal(event.data, &payload))
		assert.NotContains(t, string(event.data), "Synthetic chapter")
		if payload.BookID == bookID && payload.ProcessingStage == "chunks_ready" {
			return payload, event.id
		}
	}
}
