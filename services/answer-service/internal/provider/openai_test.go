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

	"github.com/belLena81/raglibrarian/pkg/providerhttp"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/application"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/throttle"
)

func TestOpenAIGeneratesStrictStructuredSegments(t *testing.T) {
	limit, err := throttle.New(0)
	if err != nil {
		t.Fatal(err)
	}
	var systemPrompt string
	adapter, err := NewOpenAI("https://provider", "test-model", "synthetic-key", &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Errorf("unexpected request path or authorization")
		}
		var body map[string]any
		if decodeErr := json.NewDecoder(request.Body).Decode(&body); decodeErr != nil {
			t.Errorf("decode request: %v", decodeErr)
		}
		format, ok := body["response_format"].(map[string]any)
		if !ok || format["type"] != "json_object" {
			t.Fatalf("response_format = %#v, want json_object", body["response_format"])
		}
		messages, ok := body["messages"].([]any)
		if !ok || len(messages) != 2 {
			t.Fatalf("messages = %#v", body["messages"])
		}
		first, ok := messages[0].(map[string]any)
		if !ok {
			t.Fatalf("messages[0] = %#v", messages[0])
		}
		systemPrompt, _ = first["content"].(string)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"segments\":[{\"text\":\"answer\",\"evidence_ids\":[\"e-1\"]}]}"}}]}`)),
		}, nil
	})}, limit, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	segments, err := adapter.Generate(context.Background(), application.GeneratorRequest{Question: "question", Evidence: []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "passage"}}, MaxTokens: 10})
	if err != nil || len(segments) != 1 || segments[0].Text != "answer" {
		t.Fatalf("Generate() = %#v, %v", segments, err)
	}
	assertContains := func(substr string) {
		t.Helper()
		if !strings.Contains(systemPrompt, substr) {
			t.Fatalf("system prompt %q does not contain %q", systemPrompt, substr)
		}
	}
	assertContains("Return exactly one valid JSON object and nothing else.")
	assertContains("exactly one top-level key named segments")
	assertContains("exactly two keys: text and evidence_ids")
	assertContains("Never invent evidence IDs.")
	assertContains("Example valid output:")
}

func TestOpenAIFallsBackToPlainTextCitationPreamble(t *testing.T) {
	limit, err := throttle.New(0)
	if err != nil {
		t.Fatal(err)
	}
	call := 0
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/chat/completions" {
			t.Fatalf("unexpected request path %q", request.URL.Path)
		}
		call++
		var body map[string]any
		if decodeErr := json.NewDecoder(request.Body).Decode(&body); decodeErr != nil {
			t.Fatalf("decode request: %v", decodeErr)
		}
		switch call {
		case 1:
			format, ok := body["response_format"].(map[string]any)
			if !ok || format["type"] != "json_object" {
				t.Fatalf("response_format = %#v, want json_object", body["response_format"])
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Use w, b, and dd to move and edit quickly."}}]}`)),
			}, nil
		case 2:
			if _, ok := body["response_format"]; ok {
				t.Fatalf("fallback request unexpectedly set response_format: %#v", body["response_format"])
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Citations: e-1, e-2\nAnswer: Use w, b, and dd to move and edit quickly."}}]}`)),
			}, nil
		default:
			t.Fatalf("unexpected request count %d", call)
			return nil, nil
		}
	})}
	adapter, err := NewOpenAI("https://openrouter.ai/", "model", "synthetic-key", client, limit, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	segments, err := adapter.Generate(context.Background(), application.GeneratorRequest{
		Question:  "vim shortcuts",
		Evidence:  []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "passage 1"}, {EvidenceID: "e-2", Passage: "passage 2"}},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(segments) != 1 || segments[0].Text != "Use w, b, and dd to move and edit quickly." {
		t.Fatalf("Generate() = %#v, want plain text fallback segment", segments)
	}
	if got := segments[0].EvidenceIDs; len(got) != 2 || got[0] != "e-1" || got[1] != "e-2" {
		t.Fatalf("fallback evidence ids = %#v", got)
	}
}

func TestOpenAIFallsBackToPlainTextWhenJSONModeIsRejected(t *testing.T) {
	limit, err := throttle.New(0)
	if err != nil {
		t.Fatal(err)
	}
	call := 0
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		call++
		var body map[string]any
		if decodeErr := json.NewDecoder(request.Body).Decode(&body); decodeErr != nil {
			t.Fatalf("decode request: %v", decodeErr)
		}
		switch call {
		case 1:
			format, ok := body["response_format"].(map[string]any)
			if !ok || format["type"] != "json_object" {
				t.Fatalf("response_format = %#v, want json_object", body["response_format"])
			}
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Status:     "400 Bad Request",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"response_format is not supported"}}`)),
			}, nil
		case 2:
			if _, ok := body["response_format"]; ok {
				t.Fatalf("fallback request unexpectedly set response_format: %#v", body["response_format"])
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Citations: e-1\nAnswer: Plain text fallback works."}}]}`)),
			}, nil
		default:
			t.Fatalf("unexpected request count %d", call)
			return nil, nil
		}
	})}
	adapter, err := NewOpenAI("https://openrouter.ai/", "model", "synthetic-key", client, limit, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	segments, err := adapter.Generate(context.Background(), application.GeneratorRequest{
		Question:  "vim shortcuts",
		Evidence:  []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "passage 1"}},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(segments) != 1 || segments[0].Text != "Plain text fallback works." {
		t.Fatalf("Generate() = %#v, want plain text fallback segment", segments)
	}
	if got := segments[0].EvidenceIDs; len(got) != 1 || got[0] != "e-1" {
		t.Fatalf("fallback evidence ids = %#v", got)
	}
}

func TestOpenAIAcceptsPlainTextCandidateWithCitationPreamble(t *testing.T) {
	limit, err := throttle.New(0)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Citations: e-1\nAnswer: Use w, b, and dd to move and edit quickly."}}]}`)),
		}, nil
	})}
	adapter, err := NewOpenAI("https://openrouter.ai/", "model", "synthetic-key", client, limit, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	segments, err := adapter.Generate(context.Background(), application.GeneratorRequest{
		Question:  "vim shortcuts",
		Evidence:  []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "passage 1"}},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(segments) != 1 || segments[0].Text != "Use w, b, and dd to move and edit quickly." {
		t.Fatalf("Generate() = %#v, want accepted plain text fallback", segments)
	}
	if got := segments[0].EvidenceIDs; len(got) != 1 || got[0] != "e-1" {
		t.Fatalf("fallback evidence ids = %#v", got)
	}
}

func TestOpenAIRejectsPlainTextCandidateWithoutCitationPreamble(t *testing.T) {
	limit, err := throttle.New(0)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Use w, b, and dd to move and edit quickly."}}]}`)),
		}, nil
	})}
	adapter, err := NewOpenAI("https://openrouter.ai/", "model", "synthetic-key", client, limit, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Generate(context.Background(), application.GeneratorRequest{
		Question:  "vim shortcuts",
		Evidence:  []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "passage 1"}},
		MaxTokens: 10,
	})
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
	if providerErr.ReasonCode() != "invalid_provider_response" || providerErr.ReasonDetail() != "candidate_plain_text_response" {
		t.Fatalf("reason = %s detail = %s", providerErr.ReasonCode(), providerErr.ReasonDetail())
	}
}

func TestOpenAIPreservesStructurallyValidCitationsForApplicationValidation(t *testing.T) {
	limit, err := throttle.New(0)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Citations: unknown-1\nAnswer: Use w, b, and dd to move and edit quickly."}}]}`)),
		}, nil
	})}
	adapter, err := NewOpenAI("https://openrouter.ai/", "model", "synthetic-key", client, limit, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	segments, err := adapter.Generate(context.Background(), application.GeneratorRequest{
		Question:  "vim shortcuts",
		Evidence:  []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "passage 1"}},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(segments) != 1 || len(segments[0].EvidenceIDs) != 1 || segments[0].EvidenceIDs[0] != "unknown-1" {
		t.Fatalf("Generate() = %#v, want citation retained for application validation", segments)
	}
}

func TestParsePlainTextCandidateRequiresExactlyTwoPrefixedLines(t *testing.T) {
	tests := []string{
		"Citations: e-1\nUse the command.",
		"Citations: e-1\nAnswer: Use the command.\nExtra text.",
		"Citations: e-1\n\nAnswer: Use the command.",
		"\nCitations: e-1\nAnswer: Use the command.",
		"Citations: e-1\nAnswer: Use the command.\n\n",
		"citations: e-1\nAnswer: Use the command.",
		"Citations: e-1\nanswer: Use the command.",
	}
	for _, content := range tests {
		if _, _, ok := parsePlainTextCandidate(content); ok {
			t.Fatalf("parsePlainTextCandidate(%q) unexpectedly passed", content)
		}
	}
	if citations, answer, ok := parsePlainTextCandidate("Citations: e-1, e-2\r\nAnswer: Use the command.\n"); !ok || len(citations) != 2 || answer != "Use the command." {
		t.Fatalf("valid candidate = %#v, %q, %t", citations, answer, ok)
	}
}

func TestOpenAIRejectsOversizedSerializedRequestBeforeDispatch(t *testing.T) {
	calls := 0
	policy := testPolicy()
	policy.MaximumRequestBytes = 64
	adapter, err := NewOpenAI("https://provider", "model", "synthetic-key", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected dispatch")
	})}, nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Generate(context.Background(), application.GeneratorRequest{
		Question:  "question",
		Evidence:  []domain.ContextEvidence{{EvidenceID: "e-1", Passage: strings.Repeat("x", 128)}},
		MaxTokens: 10,
	})
	if err == nil {
		t.Fatal("Generate() error = nil, want oversized request")
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
	var providerErr interface {
		ReasonCode() string
		ReasonDetail() string
	}
	if !errors.As(err, &providerErr) || providerErr.ReasonCode() != "invalid_provider_request" || providerErr.ReasonDetail() != "request_too_large" {
		t.Fatalf("error = %T %v, want stable oversized-request reason", err, err)
	}
}

func TestOpenAIRetriesCandidateFormatIncompatibilityAtMostOnce(t *testing.T) {
	calls := 0
	adapter, err := NewOpenAI("https://provider", "model", "synthetic-key", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		content := `{"segments":[]}`
		if calls == 2 {
			content = "Citations: e-1\nAnswer: Fallback answer."
		}
		body, marshalErr := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
		if marshalErr != nil {
			return nil, marshalErr
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}, nil, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	segments, err := adapter.Generate(context.Background(), application.GeneratorRequest{Question: "q", Evidence: []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "p"}}, MaxTokens: 10})
	if err != nil || len(segments) != 1 || segments[0].Text != "Fallback answer." {
		t.Fatalf("Generate() = %#v, %v", segments, err)
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want 2", calls)
	}
}

func TestOpenAIDoesNotRetryDuplicateCandidateFields(t *testing.T) {
	calls := 0
	adapter, err := NewOpenAI("https://provider", "model", "synthetic-key", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"segments\":[],\"segments\":[]}"}}]}`)),
		}, nil
	})}, nil, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.Generate(context.Background(), application.GeneratorRequest{Question: "q", Evidence: []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "p"}}, MaxTokens: 10}); err == nil {
		t.Fatal("Generate() error = nil, want duplicate candidate rejection")
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
}

func TestOpenAIDoesNotRetryNonFormatFailures(t *testing.T) {
	tests := []struct {
		name     string
		response *http.Response
		err      error
		policy   Policy
	}{
		{
			name:   "transport",
			err:    errors.New("synthetic transport failure"),
			policy: testPolicy(),
		},
		{
			name:   "cancellation",
			err:    context.Canceled,
			policy: testPolicy(),
		},
		{
			name:     "authentication",
			response: &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{}`))},
			policy:   testPolicy(),
		},
		{
			name:     "malformed envelope",
			response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`not-json`))},
			policy:   testPolicy(),
		},
		{
			name:     "duplicate envelope fields",
			response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[],"choices":[]}`))},
			policy:   testPolicy(),
		},
		{
			name:     "oversized response",
			response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 65)))},
			policy: Policy{
				MaximumRequestBytes:   128 << 10,
				MaximumResponseBytes:  64,
				MaximumCandidateBytes: 32,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			adapter, err := NewOpenAI("https://provider", "model", "synthetic-key", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return test.response, test.err
			})}, nil, test.policy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = adapter.Generate(context.Background(), application.GeneratorRequest{Question: "q", Evidence: []domain.ContextEvidence{{EvidenceID: "e-1", Passage: "p"}}, MaxTokens: 10}); err == nil {
				t.Fatal("Generate() error = nil, want provider failure")
			}
			if calls != 1 {
				t.Fatalf("provider calls = %d, want 1", calls)
			}
		})
	}
}

func TestOpenAIReportsContentFreeHTTPStatusDetails(t *testing.T) {
	limit, err := throttle.New(0)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Status:     "502 Bad Gateway",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`upstream failed`)),
		}, nil
	})}
	adapter, err := NewOpenAI("https://provider", "test-model", "synthetic-key", client, limit, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Generate(context.Background(), application.GeneratorRequest{Question: "q", Evidence: []domain.ContextEvidence{{EvidenceID: "e", Passage: "p"}}, MaxTokens: 10})
	if err == nil {
		t.Fatal("Generate() error = nil, want provider error")
	}
	var providerErr interface {
		ReasonCode() string
		ReasonDetail() string
	}
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T, want providerError", err)
	}
	if providerErr.ReasonCode() != "provider_http_status_502" || providerErr.ReasonDetail() != "provider_http_status_502" {
		t.Fatalf("reason = %s detail = %s", providerErr.ReasonCode(), providerErr.ReasonDetail())
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
		{name: "openrouter explicit port", baseURL: "https://openrouter.ai:443/", expectedPath: "/api/v1/chat/completions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := NewOpenAI(test.baseURL, "model", "key", client, nil, testPolicy())
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
		})}, nil, testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		_, err = adapter.Generate(context.Background(), application.GeneratorRequest{Question: "q", Evidence: []domain.ContextEvidence{{EvidenceID: "e", Passage: "p"}}, MaxTokens: 10})
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
			})}, nil, testPolicy())
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapter.Generate(context.Background(), application.GeneratorRequest{Question: "q", Evidence: []domain.ContextEvidence{{EvidenceID: "e", Passage: "p"}}, MaxTokens: 10})
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
		if _, err := NewOpenAI(baseURL, "model", "key", client, nil, testPolicy()); err == nil {
			t.Fatalf("base URL %q accepted", baseURL)
		}
	}
	if _, err := NewOpenAI("https://provider", "model\nother", "key", client, nil, testPolicy()); err == nil {
		t.Fatal("multiline model accepted")
	}
	if _, err := NewOpenAI("https://provider", "model", strings.Repeat(" ", 2), client, nil, testPolicy()); err == nil {
		t.Fatal("empty key accepted")
	}
	if _, err := NewOpenAI("https://provider", strings.Repeat("m", 257), "key", client, nil, testPolicy()); err == nil {
		t.Fatal("oversized model accepted")
	}
	if _, err := NewOpenAI("https://provider/"+strings.Repeat("p", 2048), "model", "key", client, nil, testPolicy()); err == nil {
		t.Fatal("oversized provider URL accepted")
	}
	for _, policy := range []Policy{
		{MaximumRequestBytes: 1<<20 + 1, MaximumResponseBytes: 1, MaximumCandidateBytes: 1},
		{MaximumRequestBytes: 1, MaximumResponseBytes: 1<<20 + 1, MaximumCandidateBytes: 1},
		{MaximumRequestBytes: 1, MaximumResponseBytes: 256 << 10, MaximumCandidateBytes: 256<<10 + 1},
	} {
		if _, err := NewOpenAI("https://provider", "model", "key", client, nil, policy); err == nil {
			t.Fatalf("unsafe provider policy accepted: %#v", policy)
		}
	}
}

func testPolicy() Policy {
	return Policy{
		MaximumRequestBytes:   128 << 10,
		MaximumResponseBytes:  128 << 10,
		MaximumCandidateBytes: 32 << 10,
	}
}

func TestProviderHTTPReadSingleLineSecretRequiresRestrictedRegularSingleLineFile(t *testing.T) {
	fileName := t.TempDir() + "/key"
	if err := os.WriteFile(fileName, []byte("synthetic-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := providerhttp.ReadSingleLineSecret(fileName, 4096); err != nil || value != "synthetic-key" {
		t.Fatalf("ReadSingleLineSecret() = %q, %v", value, err)
	}
	if err := os.Chmod(fileName, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := providerhttp.ReadSingleLineSecret(fileName, 4096); err == nil {
		t.Fatal("permissive credential file accepted")
	}
}
