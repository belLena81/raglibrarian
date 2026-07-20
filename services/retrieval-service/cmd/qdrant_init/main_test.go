package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

func TestRunCreatesMissingQdrantCollection(t *testing.T) {
	withoutRetryDelay(t)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		requests++
		if request.Header.Get("api-key") != "writer-key" {
			t.Fatalf("missing write API key header")
		}
		switch requests {
		case 1:
			if request.Method != http.MethodGet || request.URL.Path != "/collections/evidence_v2" {
				t.Fatalf("unexpected readiness request: %s %s", request.Method, request.URL.Path)
			}
			return response(http.StatusNotFound, `{}`)
		case 2:
			var body map[string]map[string]any
			if request.Method != http.MethodPut || request.URL.Path != "/collections/evidence_v2" || json.NewDecoder(request.Body).Decode(&body) != nil {
				t.Fatalf("unexpected collection creation request: %s %s %#v", request.Method, request.URL.Path, body)
			}
			if body["vectors"]["size"] != float64(domain.EmbeddingDimensions) || body["vectors"]["distance"] != "Cosine" {
				t.Fatalf("unexpected collection schema: %#v", body)
			}
			return response(http.StatusOK, `{}`)
		case 3:
			if request.Method != http.MethodGet || request.URL.Path != "/collections/evidence_v2" {
				t.Fatalf("unexpected post-create readiness request: %s %s", request.Method, request.URL.Path)
			}
			return response(http.StatusOK, `{"result":{"config":{"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
		return response(http.StatusInternalServerError, `{}`)
	})}

	err := run(context.Background(), env(map[string]string{
		"RETRIEVAL_QDRANT_URL":          "http://qdrant.test",
		"RETRIEVAL_QDRANT_API_KEY_FILE": "/run/secrets/retrieval_qdrant_api_key",
	}), secret(map[string]string{
		"/run/secrets/retrieval_qdrant_api_key": "writer-key\n",
	}), client)

	if err != nil || requests != 3 {
		t.Fatalf("run() requests=%d error=%v", requests, err)
	}
}

func TestRunAcceptsExistingQdrantCollection(t *testing.T) {
	withoutRetryDelay(t)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		requests++
		if request.Method != http.MethodGet || request.URL.Path != "/collections/evidence_v2" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		return response(http.StatusOK, `{"result":{"config":{"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`)
	})}

	err := run(context.Background(), env(map[string]string{
		"RETRIEVAL_QDRANT_URL":          "http://qdrant.test",
		"RETRIEVAL_QDRANT_API_KEY_FILE": "/run/secrets/retrieval_qdrant_api_key",
	}), secret(map[string]string{
		"/run/secrets/retrieval_qdrant_api_key": "writer-key\n",
	}), client)

	if err != nil || requests != 1 {
		t.Fatalf("run() requests=%d error=%v", requests, err)
	}
}

func TestRunRetriesTransientQdrantReadinessFailure(t *testing.T) {
	withoutRetryDelay(t)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		requests++
		switch requests {
		case 1:
			return response(http.StatusServiceUnavailable, `{}`)
		case 2:
			return response(http.StatusServiceUnavailable, `{}`)
		case 3:
			return response(http.StatusOK, `{"result":{"config":{"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`)
		default:
			t.Fatalf("unexpected request %d", requests)
			return response(http.StatusInternalServerError, `{}`)
		}
	})}

	err := run(context.Background(), env(map[string]string{
		"RETRIEVAL_QDRANT_URL":          "http://qdrant.test",
		"RETRIEVAL_QDRANT_API_KEY_FILE": "/run/secrets/retrieval_qdrant_api_key",
	}), secret(map[string]string{
		"/run/secrets/retrieval_qdrant_api_key": "writer-key\n",
	}), client)

	if err != nil || requests != 3 {
		t.Fatalf("run() requests=%d error=%v", requests, err)
	}
}

func TestLoadConfigRejectsIncompleteInitializerConfiguration(t *testing.T) {
	if _, err := loadConfig(env(map[string]string{"RETRIEVAL_QDRANT_API_KEY_FILE": "/secret"})); err == nil {
		t.Fatalf("loadConfig() accepted missing Qdrant URL")
	}
}

func TestReadSecretRejectsBlankAndMultilineValues(t *testing.T) {
	for name, contents := range map[string]string{
		"blank":     " \n",
		"multiline": "first\nsecond",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readSecret("/secret", secret(map[string]string{"/secret": contents}))
			if err == nil {
				t.Fatalf("readSecret() accepted %q secret", name)
			}
		})
	}
}

func env(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func secret(values map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		value, ok := values[path]
		if !ok {
			return nil, errors.New("test secret not found")
		}
		return []byte(value), nil
	}
}

func withoutRetryDelay(t *testing.T) {
	t.Helper()
	previousDelay := ensureRetryDelay
	ensureRetryDelay = time.Nanosecond
	t.Cleanup(func() {
		ensureRetryDelay = previousDelay
	})
}

type roundTripFunc func(*http.Request) *http.Response

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request), nil
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
	}
}
