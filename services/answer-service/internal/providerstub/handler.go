// Package providerstub provides a deterministic, content-safe HTTPS test provider.
package providerstub

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

type chatRequest struct {
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type userPayload struct {
	Evidence []struct {
		EvidenceID string `json:"evidence_id"`
	} `json:"evidence"`
}

type Scenario string

const (
	ScenarioSuccess             Scenario = "success"
	ScenarioMalformed           Scenario = "malformed"
	ScenarioUnsupportedCitation Scenario = "unsupported_citation"
	ScenarioOutage              Scenario = "outage"
	ScenarioTimeout             Scenario = "timeout"
)

type Handler struct {
	apiKey   string
	scenario Scenario
	delay    time.Duration
	calls    atomic.Uint64
}

func New(apiKey string, scenario Scenario, delay time.Duration) (*Handler, error) {
	if apiKey == "" || !validScenario(scenario) || delay < 0 || delay > 30*time.Second {
		return nil, errInvalidConfiguration{}
	}
	return &Handler{apiKey: apiKey, scenario: scenario, delay: delay}, nil
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
		http.NotFound(response, request)
		return
	}
	if subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte("Bearer "+h.apiKey)) != 1 {
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 128<<10+1))
	evidenceID, parseErr := firstEvidenceID(body)
	if err != nil || len(body) > 128<<10 || parseErr != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	h.calls.Add(1)
	if h.delay > 0 || h.scenario == ScenarioTimeout {
		delay := h.delay
		if delay == 0 {
			delay = 10 * time.Second
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-request.Context().Done():
			return
		}
	}
	if h.scenario == ScenarioOutage {
		response.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	switch h.scenario {
	case ScenarioMalformed:
		_, _ = response.Write([]byte(`{"choices":[{"message":{"content":"not-json"}}]}`))
	case ScenarioUnsupportedCitation:
		writeContent(response, `{"segments":[{"text":"unsupported","evidence_ids":["unsupported-evidence"]}]}`)
	default:
		candidate, marshalErr := json.Marshal(map[string]any{"segments": []any{map[string]any{
			"text": "Deterministic grounded answer.", "evidence_ids": []string{evidenceID},
		}}})
		if marshalErr != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeContent(response, string(candidate))
	}
}

func firstEvidenceID(body []byte) (string, error) {
	var envelope chatRequest
	if !json.Valid(body) || json.Unmarshal(body, &envelope) != nil {
		return "", errInvalidRequest{}
	}
	for _, message := range envelope.Messages {
		if message.Role != "user" {
			continue
		}
		var payload userPayload
		if json.Unmarshal([]byte(message.Content), &payload) != nil || len(payload.Evidence) == 0 || payload.Evidence[0].EvidenceID == "" {
			return "", errInvalidRequest{}
		}
		return payload.Evidence[0].EvidenceID, nil
	}
	return "", errInvalidRequest{}
}

func (h *Handler) Calls() uint64 { return h.calls.Load() }

func writeContent(response http.ResponseWriter, content string) {
	_ = json.NewEncoder(response).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
}

func validScenario(value Scenario) bool {
	switch value {
	case ScenarioSuccess, ScenarioMalformed, ScenarioUnsupportedCitation, ScenarioOutage, ScenarioTimeout:
		return true
	default:
		return false
	}
}

type errInvalidConfiguration struct{}

func (errInvalidConfiguration) Error() string { return "invalid provider stub configuration" }

type errInvalidRequest struct{}

func (errInvalidRequest) Error() string { return "invalid provider stub request" }
