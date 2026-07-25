package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/throttle"
	"go.uber.org/zap"
)

func TestOpenAIGenerateSummarizesText(t *testing.T) {
	var requestPath string
	var requestModel string
	var requestContent string
	limit, err := throttle.New(0)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewOpenAI("https://openrouter.ai/", "test-model", "synthetic-key", &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requestPath = request.URL.Path
		requestModel, requestContent = decodeSummaryRequest(t, request.Body)
		return httpResponse(http.StatusOK, `{"choices":[{"message":{"content":" concise summary "}}]}`), nil
	})}, zap.NewNop(), limit)
	if err != nil {
		t.Fatalf("NewOpenAI() error = %v", err)
	}

	summary, err := adapter.Summarize(context.Background(), application.SummaryRequest{Question: "How do retries help?", Passage: "  Deterministic retries keep search stable.  "})
	if err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if summary != "concise summary" {
		t.Fatalf("Summarize() = %q, want concise summary", summary)
	}
	if requestPath != "/api/v1/chat/completions" || requestModel != "test-model" || !strings.Contains(requestContent, "How do retries help?") || !strings.Contains(requestContent, "Deterministic retries keep search stable.") {
		t.Fatalf("unexpected request: path=%q model=%q content=%q", requestPath, requestModel, requestContent)
	}
}

func TestOpenAIChatCompletionPathNormalization(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		expected string
	}{
		{name: "host", baseURL: "https://provider", expected: "/v1/chat/completions"},
		{name: "v1", baseURL: "https://provider/v1", expected: "/v1/chat/completions"},
		{name: "openrouter root", baseURL: "https://openrouter.ai/", expected: "/api/v1/chat/completions"},
		{name: "openrouter api v1", baseURL: "https://openrouter.ai/api/v1", expected: "/api/v1/chat/completions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := NewOpenAI(test.baseURL, "model", "key", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return httpResponse(http.StatusOK, `{"choices":[{"message":{"content":"summary"}}]}`), nil
			})}, zap.NewNop(), nil)
			if err != nil {
				t.Fatalf("NewOpenAI() error = %v", err)
			}
			if adapter.endpoint.Path != test.expected {
				t.Fatalf("endpoint.Path = %q, want %q", adapter.endpoint.Path, test.expected)
			}
		})
	}
}

func TestNewOpenAIRejectsInvalidConfiguration(t *testing.T) {
	client := &http.Client{}
	if _, err := NewOpenAI("http://provider", "model", "key", client, zap.NewNop(), nil); err == nil {
		t.Fatal("NewOpenAI accepted a non-HTTPS base URL")
	}
	if _, err := NewOpenAI("https://provider", " ", "key", client, zap.NewNop(), nil); err == nil {
		t.Fatal("NewOpenAI accepted an empty model")
	}
	if _, err := NewOpenAI("https://provider", "model", " ", client, zap.NewNop(), nil); err == nil {
		t.Fatal("NewOpenAI accepted an empty API key")
	}
}

func TestOpenAIRequestFailureLogsDiagnostics(t *testing.T) {
	var output strings.Builder
	log, err := logger.NewWithWriter(&output)
	if err != nil {
		t.Fatal(err)
	}
	limit, err := throttle.New(0)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewOpenAI("https://openrouter.ai/", "test-model", "synthetic-key", &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/chat/completions" {
			t.Fatalf("request.URL.Path = %q", request.URL.Path)
		}
		return httpResponse(http.StatusUnauthorized, `{"error":{"message":"Missing Authentication header","code":401}}`), nil
	})}, log, limit)
	if err != nil {
		t.Fatalf("NewOpenAI() error = %v", err)
	}

	_, err = adapter.Summarize(context.Background(), application.SummaryRequest{
		Question: "How do retries help?",
		Passage:  "Deterministic retries keep search stable.",
	})
	if err == nil {
		t.Fatal("Summarize() error = nil, want provider failure")
	}

	value := output.String()
	assertContains := func(substr string) {
		t.Helper()
		if !strings.Contains(value, substr) {
			t.Fatalf("log output %q does not contain %q", value, substr)
		}
	}
	assertContains("retrieval summary request failed")
	assertContains("reason_code=provider_http_status_401")
	assertContains("request_model=test-model")
	assertContains("request_url=https://openrouter.ai/api/v1/chat/completions")
	assertContains("request_path=/api/v1/chat/completions")
	assertContains("request_body_preview={\"model\":\"test-model\"")
	assertContains("response_body_preview={\"error\":{\"message\":\"Missing Authentication header\",\"code\":401}}")
	if !regexp.MustCompile(`request_body_sha256=[0-9a-f]{64}`).MatchString(value) {
		t.Fatalf("log output %q does not contain a request body digest", value)
	}
	if !regexp.MustCompile(`stack_trace=.*openai\.go`).MatchString(value) {
		t.Fatalf("log output %q does not contain a compact stack trace", value)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func httpResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func decodeSummaryRequest(t *testing.T, body io.ReadCloser) (string, string) {
	t.Helper()
	defer func() { _ = body.Close() }()
	contents, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	var request struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(contents, &request); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(request.Messages) != 2 {
		t.Fatalf("messages = %#v", request.Messages)
	}
	return request.Model, request.Messages[1].Content
}
