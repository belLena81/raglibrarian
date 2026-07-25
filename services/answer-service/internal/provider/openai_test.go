package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/throttle"
)

func TestOpenAIGeneratesStrictStructuredSegments(t *testing.T) {
	limit, err := throttle.New(0)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewOpenAI("https://provider", "test-model", "synthetic-key", &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Errorf("unexpected request path or authorization")
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"segments\":[{\"text\":\"answer\",\"evidence_ids\":[\"e-1\"]}]}"}}]}`)),
		}, nil
	})}, limit)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := adapter.Generate(context.Background(), application.ProviderRequest{Question: "question", Evidence: []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "passage"}}, MaxTokens: 10})
	if err != nil || len(segments) != 1 || segments[0].Text != "answer" {
		t.Fatalf("Generate() = %#v, %v", segments, err)
	}
}

func TestOpenAIAcceptsPlainTextCandidateContent(t *testing.T) {
	limit, err := throttle.New(0)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/chat/completions" {
			t.Fatalf("unexpected request path %q", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Use w, b, and dd to move and edit quickly."}}]}`)),
		}, nil
	})}
	adapter, err := NewOpenAI("https://openrouter.ai/", "model", "synthetic-key", client, limit)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := adapter.Generate(context.Background(), application.ProviderRequest{
		Question:  "vim shortcuts",
		Evidence:  []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "passage 1"}, {EvidenceID: "e-2", Passage: "passage 2"}},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 {
		t.Fatalf("len(segments) = %d, want 1", len(segments))
	}
	if segments[0].Text != "Use w, b, and dd to move and edit quickly." {
		t.Fatalf("segment text = %q", segments[0].Text)
	}
	if got, want := strings.Join(segments[0].EvidenceIDs, ","), "e-1,e-2"; got != want {
		t.Fatalf("segment evidence IDs = %q, want %q", got, want)
	}
}

func TestNewOpenAIBuildsChatCompletionsEndpointFromAPIBase(t *testing.T) {
	client := &http.Client{}
	for _, test := range []struct {
		name         string
		baseURL      string
		expectedPath string
	}{
		{name: "host", baseURL: "https://provider", expectedPath: "/v1/chat/completions"},
		{name: "v1", baseURL: "https://provider/v1", expectedPath: "/v1/chat/completions"},
		{name: "v1 trailing slash", baseURL: "https://provider/v1/", expectedPath: "/v1/chat/completions"},
		{name: "prefixed v1", baseURL: "https://provider/openai/v1", expectedPath: "/openai/v1/chat/completions"},
		{name: "openrouter root", baseURL: "https://openrouter.ai/", expectedPath: "/api/v1/chat/completions"},
		{name: "openrouter api v1", baseURL: "https://openrouter.ai/api/v1", expectedPath: "/api/v1/chat/completions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := NewOpenAI(test.baseURL, "model", "key", client, nil)
			if err != nil {
				t.Fatal(err)
			}
			if adapter.endpoint.Path != test.expectedPath {
				t.Fatalf("endpoint path = %q, want %q", adapter.endpoint.Path, test.expectedPath)
			}
		})
	}
}

func TestOpenAIRejectsRedirectUnknownAndDuplicateCandidateFields(t *testing.T) {
	responses := []struct {
		status int
		body   string
	}{
		{status: http.StatusTemporaryRedirect, body: `{}`},
		{status: http.StatusOK, body: `{"choices":[{"message":{"content":"{\"segments\":[],\"unknown\":true}"}}]}`},
		{status: http.StatusOK, body: `{"choices":[{"message":{"content":"{\"segments\":[],\"segments\":[]}"}}]}`},
	}
	for index, fixture := range responses {
		adapter, err := NewOpenAI("https://provider", "test-model", "synthetic-key", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: fixture.status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(fixture.body)),
			}, nil
		})}, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = adapter.Generate(context.Background(), application.ProviderRequest{Question: "q", Evidence: []domain.ContextEvidence{{EvidenceID: "e", Passage: "p"}}, MaxTokens: 10})
		if err == nil {
			t.Fatalf("case %d unexpectedly passed", index)
		}
	}
}

func TestOpenAIReportsSanitizedInvalidProviderResponseDetails(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		detail string
	}{
		{name: "duplicate fields", body: `{"choices":[{"message":{"content":"x","content":"y"}}]}`, detail: "duplicate_object_fields"},
		{name: "choice count", body: `{"choices":[]}`, detail: "unexpected_choices_count_0"},
		{name: "json shape", body: `{"choices":[{"message":{"content":"{\"segments\":[]}"}}]}`, detail: "candidate_json_shape_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := NewOpenAI("https://provider", "test-model", "synthetic-key", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(test.body)),
				}, nil
			})}, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapter.Generate(context.Background(), application.ProviderRequest{Question: "q", Evidence: []domain.ContextEvidence{{EvidenceID: "e", Passage: "p"}}, MaxTokens: 10})
			if err == nil {
				t.Fatal("Generate() error = nil, want invalid provider response")
			}
			var providerErr interface {
				ReasonCode() string
				ReasonDetail() string
			}
			if !errors.As(err, &providerErr) {
				t.Fatalf("error = %T, want providerError", err)
			}
			if providerErr.ReasonCode() != "invalid_provider_response" || providerErr.ReasonDetail() != test.detail {
				t.Fatalf("reason = %s detail = %s", providerErr.ReasonCode(), providerErr.ReasonDetail())
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestNewOpenAIRequiresHTTPSAndFixedConfiguration(t *testing.T) {
	client := &http.Client{}
	for _, baseURL := range []string{"http://provider", "https://user:pass@provider", "https://provider?key=value", "https://provider#fragment"} {
		if _, err := NewOpenAI(baseURL, "model", "key", client, nil); err == nil {
			t.Fatalf("base URL %q accepted", baseURL)
		}
	}
	if _, err := NewOpenAI("https://provider", "model\nother", "key", client, nil); err == nil {
		t.Fatal("multiline model accepted")
	}
	if _, err := NewOpenAI("https://provider", "model", strings.Repeat(" ", 2), client, nil); err == nil {
		t.Fatal("empty key accepted")
	}
	if _, err := NewOpenAI("https://provider", strings.Repeat("m", 257), "key", client, nil); err == nil {
		t.Fatal("oversized model accepted")
	}
	if _, err := NewOpenAI("https://provider/"+strings.Repeat("p", 2048), "model", "key", client, nil); err == nil {
		t.Fatal("oversized provider URL accepted")
	}
}

func TestReadAPIKeyRequiresRestrictedRegularSingleLineFile(t *testing.T) {
	fileName := t.TempDir() + "/key"
	if err := os.WriteFile(fileName, []byte("synthetic-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := ReadAPIKey(fileName); err != nil || value != "synthetic-key" {
		t.Fatalf("ReadAPIKey() = %q, %v", value, err)
	}
	if err := os.Chmod(fileName, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAPIKey(fileName); err == nil {
		t.Fatal("permissive credential file accepted")
	}
}
