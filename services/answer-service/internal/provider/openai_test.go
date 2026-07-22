package provider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
)

func TestOpenAIGeneratesStrictStructuredSegments(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Errorf("unexpected request path or authorization")
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = response.Write([]byte(`{"choices":[{"message":{"content":"{\"segments\":[{\"text\":\"answer\",\"evidence_ids\":[\"e-1\"]}]}"}}]}`))
	}))
	defer server.Close()
	client := server.Client()
	client.Transport.(*http.Transport).TLSClientConfig.MinVersion = tls.VersionTLS13
	adapter, err := NewOpenAI(server.URL, "test-model", "synthetic-key", client)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := adapter.Generate(context.Background(), application.ProviderRequest{Question: "question", Evidence: []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "passage"}}, MaxTokens: 10})
	if err != nil || len(segments) != 1 || segments[0].Text != "answer" {
		t.Fatalf("Generate() = %#v, %v", segments, err)
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
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := NewOpenAI(test.baseURL, "model", "key", client)
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
		server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(fixture.status)
			_, _ = response.Write([]byte(fixture.body))
		}))
		adapter, err := NewOpenAI(server.URL, "test-model", "synthetic-key", server.Client())
		if err != nil {
			t.Fatal(err)
		}
		_, err = adapter.Generate(context.Background(), application.ProviderRequest{Question: "q", Evidence: []domain.ContextEvidence{{EvidenceID: "e", Passage: "p"}}, MaxTokens: 10})
		server.Close()
		if err == nil {
			t.Fatalf("case %d unexpectedly passed", index)
		}
	}
}

func TestNewOpenAIRequiresHTTPSAndFixedConfiguration(t *testing.T) {
	client := &http.Client{}
	for _, baseURL := range []string{"http://provider", "https://user:pass@provider", "https://provider?key=value", "https://provider#fragment"} {
		if _, err := NewOpenAI(baseURL, "model", "key", client); err == nil {
			t.Fatalf("base URL %q accepted", baseURL)
		}
	}
	if _, err := NewOpenAI("https://provider", "model\nother", "key", client); err == nil {
		t.Fatal("multiline model accepted")
	}
	if _, err := NewOpenAI("https://provider", "model", strings.Repeat(" ", 2), client); err == nil {
		t.Fatal("empty key accepted")
	}
	if _, err := NewOpenAI("https://provider", strings.Repeat("m", 257), "key", client); err == nil {
		t.Fatal("oversized model accepted")
	}
	if _, err := NewOpenAI("https://provider/"+strings.Repeat("p", 2048), "model", "key", client); err == nil {
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
