package providerstub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerRequiresCredentialAndReturnsDeterministicResponse(t *testing.T) {
	handler, err := New("synthetic-key", ScenarioSuccess, 0)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"messages":[
			{"role":"system","content":"fixed policy"},
			{"role":"user","content":"{\"question\":\"q\",\"evidence\":[{\"evidence_id\":\"book-1:chunk-9\",\"passage\":\"stored passage\"}]}"}
		]
	}`))
	request.Header.Set("Authorization", "Bearer synthetic-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "book-1:chunk-9") || handler.Calls() != 1 {
		t.Fatalf("status=%d body=%q calls=%d", response.Code, response.Body.String(), handler.Calls())
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))
	if unauthorized.Code != http.StatusUnauthorized || handler.Calls() != 1 {
		t.Fatalf("unauthorized status=%d calls=%d", unauthorized.Code, handler.Calls())
	}
}

func TestHandlerHealthDoesNotCountAsProviderCall(t *testing.T) {
	handler, err := New("synthetic-key", ScenarioSuccess, 0)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNoContent || handler.Calls() != 0 {
		t.Fatalf("status=%d calls=%d", response.Code, handler.Calls())
	}
}

func TestHandlerRejectsSuccessRequestWithoutUsableEvidence(t *testing.T) {
	handler, err := New("synthetic-key", ScenarioSuccess, 0)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	request.Header.Set("Authorization", "Bearer synthetic-key")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || handler.Calls() != 0 {
		t.Fatalf("status=%d calls=%d", response.Code, handler.Calls())
	}
}
