package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestTEIEmbedsWithProviderTruncationEnabled(t *testing.T) {
	client, err := NewTEI("https://tei.example", &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/embed" || request.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var body struct {
			Inputs   string `json:"inputs"`
			Truncate bool   `json:"truncate"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Inputs != bgeQueryInstruction+"replication" || !body.Truncate {
			t.Fatalf("unexpected body: %#v", body)
		}
		vector := make([]float32, domain.EmbeddingDimensions)
		payload, err := json.Marshal([][]float32{vector})
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		return httpResponse(http.StatusOK, string(payload)), nil
	})}, zap.NewNop(), nil)
	if err != nil {
		t.Fatalf("NewTEI() error = %v", err)
	}
	vector, err := client.EmbedQuery(context.Background(), "replication")
	if err != nil || len(vector) != domain.EmbeddingDimensions {
		t.Fatalf("EmbedQuery() length = %d, error = %v", len(vector), err)
	}
}

func TestTEILogsTransportFailuresWithReasonDetails(t *testing.T) {
	observed, logs := observer.New(zap.WarnLevel)
	client, err := NewTEI("https://tei.example", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 10.0.0.2:8080: connect: connection refused")
	})}, zap.New(observed), nil)
	if err != nil {
		t.Fatalf("NewTEI() error = %v", err)
	}

	_, err = client.EmbedQuery(context.Background(), "replication")
	if err == nil {
		t.Fatal("EmbedQuery() error = nil")
	}
	entries := logs.All()
	if len(entries) == 0 {
		t.Fatal("expected failure log entry")
	}
	last := entries[len(entries)-1]
	if last.Message != "retrieval.embedding.request.failed" {
		t.Fatalf("message = %q, want failure log", last.Message)
	}
	fields := last.ContextMap()
	if fields["reason_code"] != "provider_network_error" || fields["stage"] != "request" || fields["operation"] != "embed_query" {
		t.Fatalf("unexpected fields: %#v", fields)
	}
	reasonDetail, ok := fields["reason_detail"].(string)
	if !ok || !strings.Contains(reasonDetail, "connection refused") {
		t.Fatalf("reason_detail = %#v", fields["reason_detail"])
	}
}

func TestTEILogsHTTPStatusFailures(t *testing.T) {
	observed, logs := observer.New(zap.InfoLevel)
	client, err := NewTEI("https://tei.example", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return httpResponse(http.StatusServiceUnavailable, `{"error":"overloaded"}`), nil
	})}, zap.New(observed), nil)
	if err != nil {
		t.Fatalf("NewTEI() error = %v", err)
	}

	_, err = client.EmbedDocuments(context.Background(), []string{"first", "second"})
	if err == nil {
		t.Fatal("EmbedDocuments() error = nil")
	}
	entries := logs.FilterMessage("retrieval.embedding.request.failed").All()
	if len(entries) == 0 {
		t.Fatal("expected failure log entry")
	}
	fields := entries[len(entries)-1].ContextMap()
	if fields["reason_code"] != "provider_http_status_503" || fields["status_code"] != int64(503) || fields["operation"] != "embed_documents" {
		t.Fatalf("unexpected fields: %#v", fields)
	}
	if fields["reason_detail"] != "{\"error\":\"overloaded\"}" {
		t.Fatalf("reason_detail = %#v", fields["reason_detail"])
	}
}

func TestTEIEmbedDocumentsBatchesEightInputsAndPreservesOrder(t *testing.T) {
	requests := make([][]string, 0, 2)
	client, err := NewTEI("https://tei.example", &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Inputs   []string `json:"inputs"`
			Truncate bool     `json:"truncate"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !body.Truncate {
			t.Fatalf("expected truncation enabled: %#v", body)
		}
		requests = append(requests, append([]string(nil), body.Inputs...))
		vectors := make([][]float32, 0, len(body.Inputs))
		for _, input := range body.Inputs {
			vector := make([]float32, domain.EmbeddingDimensions)
			vector[0] = float32(len(input))
			vectors = append(vectors, vector)
		}
		payload, err := json.Marshal(vectors)
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		return httpResponse(http.StatusOK, string(payload)), nil
	})}, zap.NewNop(), nil)
	if err != nil {
		t.Fatalf("NewTEI() error = %v", err)
	}

	inputs := []string{"1", "22", "333", "4444", "55555", "666666", "7777777", "88888888", "999999999"}
	vectors, err := client.EmbedDocuments(context.Background(), inputs)
	if err != nil {
		t.Fatalf("EmbedDocuments() error = %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if !slices.Equal(requests[0], inputs[:8]) {
		t.Fatalf("first request = %#v, want %#v", requests[0], inputs[:8])
	}
	if !slices.Equal(requests[1], inputs[8:]) {
		t.Fatalf("second request = %#v, want %#v", requests[1], inputs[8:])
	}
	if len(vectors) != len(inputs) {
		t.Fatalf("vector count = %d, want %d", len(vectors), len(inputs))
	}
	for index, input := range inputs {
		if got := vectors[index][0]; got != float32(len(input)) {
			t.Fatalf("vector[%d][0] = %v, want %d", index, got, len(input))
		}
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
