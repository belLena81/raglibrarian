package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/pkg/process"
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
			if body["vectors"]["size"] != float64(domain.EmbeddingDimensions) || body["vectors"]["distance"] != "Cosine" ||
				body["metadata"]["raglibrarian_index_profile_digest"] != supportedProfileDigestHex() ||
				body["metadata"]["raglibrarian_collection_schema_digest"] != supportedCollectionSchemaDigestHex() {
				t.Fatalf("unexpected collection schema: %#v", body)
			}
			return response(http.StatusOK, `{}`)
		case 3:
			if request.Method != http.MethodGet || request.URL.Path != "/collections/evidence_v2" {
				t.Fatalf("unexpected post-create readiness request: %s %s", request.Method, request.URL.Path)
			}
			return response(http.StatusOK, `{"result":{"config":{"metadata":{"raglibrarian_index_profile_digest":"`+supportedProfileDigestHex()+`"},"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`)
		case 4, 5, 6, 7, 8, 9, 10:
			assertFieldIndexRequest(t, request, requests-4)
			return response(http.StatusOK, `{}`)
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

	if err != nil || requests != 10 {
		t.Fatalf("run() requests=%d error=%v", requests, err)
	}
}

func TestRunAcceptsExistingQdrantCollection(t *testing.T) {
	withoutRetryDelay(t)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		requests++
		switch requests {
		case 1:
			if request.Method != http.MethodGet || request.URL.Path != "/collections/evidence_v2" {
				t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
			}
			return response(http.StatusOK, `{"result":{"config":{"metadata":{"raglibrarian_index_profile_digest":"`+supportedProfileDigestHex()+`"},"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`)
		case 2:
			assertCollectionMetadataRequest(t, request)
			return response(http.StatusOK, `{}`)
		case 3, 4, 5, 6, 7, 8, 9:
			assertFieldIndexRequest(t, request, requests-3)
			return response(http.StatusOK, `{}`)
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

	if err != nil || requests != 9 {
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
			return response(http.StatusOK, `{"result":{"config":{"metadata":{"raglibrarian_index_profile_digest":"`+supportedProfileDigestHex()+`"},"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`)
		case 4:
			assertCollectionMetadataRequest(t, request)
			return response(http.StatusOK, `{}`)
		case 5, 6, 7, 8, 9, 10, 11:
			assertFieldIndexRequest(t, request, requests-5)
			return response(http.StatusOK, `{}`)
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

	if err != nil || requests != 11 {
		t.Fatalf("run() requests=%d error=%v", requests, err)
	}
}

func TestRunDropsPrivilegesBeforeCreatingQdrantClient(t *testing.T) {
	withoutRetryDelay(t)
	steps := make([]string, 0, 8)
	previousDrop := dropPrivileges
	dropPrivileges = func(identity process.Identity) error {
		if identity != (process.Identity{UID: 123, GID: 456}) {
			t.Fatalf("dropPrivileges() identity = %#v", identity)
		}
		steps = append(steps, "drop")
		return nil
	}
	t.Cleanup(func() {
		dropPrivileges = previousDrop
	})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		steps = append(steps, request.Method)
		switch len(steps) {
		case 2:
			return response(http.StatusOK, `{"result":{"config":{"metadata":{"raglibrarian_index_profile_digest":"`+supportedProfileDigestHex()+`"},"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`)
		case 3:
			assertCollectionMetadataRequest(t, request)
			return response(http.StatusOK, `{}`)
		case 4, 5, 6, 7, 8, 9, 10:
			assertFieldIndexRequest(t, request, len(steps)-4)
			return response(http.StatusOK, `{}`)
		default:
			t.Fatalf("unexpected request sequence: %#v", steps)
			return response(http.StatusInternalServerError, `{}`)
		}
	})}

	err := run(context.Background(), env(map[string]string{
		"RETRIEVAL_QDRANT_URL":          "http://qdrant.test",
		"RETRIEVAL_QDRANT_API_KEY_FILE": "/run/secrets/retrieval_qdrant_api_key",
		"RUN_AS_UID":                    "123",
		"RUN_AS_GID":                    "456",
	}), secret(map[string]string{
		"/run/secrets/retrieval_qdrant_api_key": "writer-key\n",
	}), client)

	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	want := []string{"drop", "GET", "PATCH", "PUT", "PUT", "PUT", "PUT", "PUT", "PUT", "PUT"}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("run() steps = %#v, want %#v", steps, want)
	}
}

func TestLoadConfigRejectsIncompleteInitializerConfiguration(t *testing.T) {
	if _, err := loadConfig(env(map[string]string{"RETRIEVAL_QDRANT_API_KEY_FILE": "/secret"})); err == nil {
		t.Fatalf("loadConfig() accepted missing Qdrant URL")
	}
}

func TestLoadConfigRejectsInvalidRuntimeIdentity(t *testing.T) {
	if _, err := loadConfig(env(map[string]string{
		"RETRIEVAL_QDRANT_URL":          "http://qdrant.test",
		"RETRIEVAL_QDRANT_API_KEY_FILE": "/secret",
		"RUN_AS_UID":                    "0",
	})); err == nil {
		t.Fatal("loadConfig() accepted invalid UID")
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

func supportedProfileDigestHex() string {
	digest := domain.SupportedIndexProfile().Digest
	return hex.EncodeToString(digest[:])
}

func supportedCollectionSchemaDigestHex() string {
	digest := domain.CollectionSchemaDigest()
	return hex.EncodeToString(digest[:])
}

func assertCollectionMetadataRequest(t *testing.T, request *http.Request) {
	t.Helper()
	var body map[string]map[string]string
	if request.Method != http.MethodPatch || request.URL.Path != "/collections/evidence_v2" ||
		json.NewDecoder(request.Body).Decode(&body) != nil ||
		body["metadata"]["raglibrarian_index_profile_digest"] != supportedProfileDigestHex() ||
		body["metadata"]["raglibrarian_collection_schema_digest"] != supportedCollectionSchemaDigestHex() {
		t.Fatalf("unexpected collection metadata request: %s %s %#v", request.Method, request.URL.Path, body)
	}
}

func assertFieldIndexRequest(t *testing.T, request *http.Request, index int) {
	t.Helper()
	expected := []struct {
		name   string
		schema string
	}{
		{name: "indexed", schema: "keyword"},
		{name: "vector_kind", schema: "keyword"},
		{name: "job_id", schema: "keyword"},
		{name: "book_id", schema: "keyword"},
		{name: "author_normalized", schema: "keyword"},
		{name: "tags_normalized", schema: "keyword"},
		{name: "year", schema: "integer"},
	}
	var body map[string]string
	if request.Method != http.MethodPut || request.URL.Path != "/collections/evidence_v2/index" || json.NewDecoder(request.Body).Decode(&body) != nil {
		t.Fatalf("unexpected field index request: %s %s %#v", request.Method, request.URL.Path, body)
	}
	if body["field_name"] != expected[index].name || body["field_schema"] != expected[index].schema {
		t.Fatalf("unexpected field index body: %#v", body)
	}
}
