package handler

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	edgemiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

const (
	testSSEHeartbeat    = 50 * time.Millisecond
	testSSEWriteTimeout = 20 * time.Millisecond
)

type sseSessionStub struct {
	principal authflow.Principal
}

func (s sseSessionStub) ValidateSession(context.Context, string, string) (authflow.Principal, error) {
	return s.principal, nil
}

type adminSSEUseCaseStub struct {
	sseSessionStub
}

func (adminSSEUseCaseStub) ListPending(context.Context, authflow.Principal, int, string) (authflow.PendingPage, error) {
	return authflow.PendingPage{}, nil
}

func (adminSSEUseCaseStub) Approve(context.Context, authflow.Principal, string) error {
	return nil
}

func (adminSSEUseCaseStub) Reject(context.Context, authflow.Principal, string) error {
	return nil
}

func TestBookEventsSurvivesServerAndFrameWriteDeadlines(t *testing.T) {
	principal := authflow.Principal{
		UserID:    "reader-1",
		SessionID: "session-1",
		Role:      "reader",
		Status:    "active",
	}
	hub := NewBookStatusHub(1)
	hub.SetAvailable(true)
	handler := &BooksHandler{events: &bookEvents{
		sessions: sseSessionStub{principal: principal},
		hub:      hub,
		timing:   testSSETiming(),
	}}

	assertSSEHeartbeatsSurviveWriteTimeout(t, func(w http.ResponseWriter, r *http.Request) {
		ctx := edgemiddleware.WithClaims(r.Context(), auth.Claims{
			UserID:    principal.UserID,
			SessionID: principal.SessionID,
			Role:      auth.RoleReader,
			ExpiresAt: time.Now().Add(time.Minute),
		})
		handler.Events(w, r.WithContext(ctx))
	})
}

func TestBookEventsSendsOverflowResyncForSlowSubscriber(t *testing.T) {
	principal := authflow.Principal{
		UserID:    "reader-1",
		SessionID: "session-1",
		Role:      "reader",
		Status:    "active",
	}
	hub := NewBookStatusHub(1)
	hub.SetAvailable(true)
	handler := &BooksHandler{events: &bookEvents{
		sessions: sseSessionStub{principal: principal},
		hub:      hub,
		timing: sseTiming{
			heartbeatInterval:  time.Second,
			revalidateInterval: time.Second,
			maximumDuration:    time.Second,
			writeTimeout:       testSSEWriteTimeout,
		},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := edgemiddleware.WithClaims(r.Context(), auth.Claims{
			UserID:    principal.UserID,
			SessionID: principal.SessionID,
			Role:      auth.RoleReader,
			ExpiresAt: time.Now().Add(time.Minute),
		})
		handler.Events(w, r.WithContext(ctx))
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("SSE response status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	scanner := bufio.NewScanner(response.Body)
	if !scanner.Scan() || scanner.Text() != "event: books-resync" {
		t.Fatalf("first SSE line = %q, want initial books-resync", scanner.Text())
	}
	if !scanner.Scan() || scanner.Text() != "data: {\"version\":1}" {
		t.Fatalf("initial SSE data = %q, want version", scanner.Text())
	}
	if !scanner.Scan() {
		t.Fatal("initial SSE frame was incomplete")
	}

	for book := 0; book <= maxPendingBookStatusEvents; book++ {
		hub.Publish(BookStatusEvent{BookID: string(rune(book + 1)), ProcessingVersion: 1})
	}
	if !scanner.Scan() || scanner.Text() != "event: books-resync" {
		t.Fatalf("overflow SSE event = %q, want books-resync", scanner.Text())
	}
	if !scanner.Scan() || scanner.Text() != "data: {\"version\":1,\"reason\":\"subscriber_overflow\"}" {
		t.Fatalf("overflow SSE data = %q, want subscriber-overflow resync", scanner.Text())
	}
}

func TestAdminEventsSurvivesServerAndFrameWriteDeadlines(t *testing.T) {
	principal := authflow.Principal{
		UserID:    "admin-1",
		SessionID: "session-1",
		Role:      "admin",
		Status:    "active",
	}
	handler := NewAdminHandler(adminSSEUseCaseStub{
		sseSessionStub: sseSessionStub{principal: principal},
	}, NewPendingHub(1))
	handler.timing = testSSETiming()

	assertSSEHeartbeatsSurviveWriteTimeout(t, func(w http.ResponseWriter, r *http.Request) {
		ctx := edgemiddleware.WithClaims(r.Context(), auth.Claims{
			UserID:    principal.UserID,
			SessionID: principal.SessionID,
			Role:      auth.RoleAdmin,
			ExpiresAt: time.Now().Add(time.Minute),
		})
		handler.Events(w, r.WithContext(ctx))
	})
}

func TestAdminEventsEndsAtAccessTokenExpiry(t *testing.T) {
	principal := authflow.Principal{
		UserID:    "admin-1",
		SessionID: "session-1",
		Role:      "admin",
		Status:    "active",
	}
	handler := NewAdminHandler(adminSSEUseCaseStub{
		sseSessionStub: sseSessionStub{principal: principal},
	}, NewPendingHub(1))
	handler.timing = sseTiming{
		heartbeatInterval:  20 * time.Millisecond,
		revalidateInterval: time.Second,
		maximumDuration:    time.Second,
		writeTimeout:       testSSEWriteTimeout,
	}
	tokenLifetime := 120 * time.Millisecond
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := edgemiddleware.WithClaims(r.Context(), auth.Claims{
			UserID:    principal.UserID,
			SessionID: principal.SessionID,
			Role:      auth.RoleAdmin,
			ExpiresAt: time.Now().Add(tokenLifetime),
		})
		handler.Events(w, r.WithContext(ctx))
	}))
	server.Start()
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	startedAt := time.Now()
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("SSE response status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	scanner := bufio.NewScanner(response.Body)
	heartbeats := 0
	for scanner.Scan() {
		if scanner.Text() == ": heartbeat" {
			heartbeats++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read SSE stream: %v", err)
	}
	if heartbeats == 0 {
		t.Fatal("SSE stream ended before its first heartbeat")
	}
	if elapsed := time.Since(startedAt); elapsed >= 500*time.Millisecond {
		t.Fatalf("SSE stream lasted %s, want token-bound termination before 500ms", elapsed)
	}
}

func TestAdminEventsRejectsMissingOrExpiredClaims(t *testing.T) {
	principal := authflow.Principal{
		UserID:    "admin-1",
		SessionID: "session-1",
		Role:      "admin",
		Status:    "active",
	}
	tests := []struct {
		name    string
		context func(context.Context) context.Context
	}{
		{
			name: "missing claims",
			context: func(ctx context.Context) context.Context {
				return edgemiddleware.WithPrincipal(ctx, principal)
			},
		},
		{
			name: "expired claims",
			context: func(ctx context.Context) context.Context {
				return edgemiddleware.WithClaims(ctx, auth.Claims{
					UserID:    principal.UserID,
					SessionID: principal.SessionID,
					Role:      auth.RoleAdmin,
					ExpiresAt: time.Now().Add(-time.Second),
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewAdminHandler(adminSSEUseCaseStub{
				sseSessionStub: sseSessionStub{principal: principal},
			}, NewPendingHub(1))
			request := httptest.NewRequest(http.MethodGet, "/admin/events", nil)
			request = request.WithContext(test.context(request.Context()))
			response := httptest.NewRecorder()

			handler.Events(response, request)

			if response.Code != http.StatusForbidden {
				t.Fatalf("SSE response status = %d, want %d", response.Code, http.StatusForbidden)
			}
			if body := response.Body.String(); !strings.Contains(body, `"code":"forbidden"`) {
				t.Fatalf("SSE response body = %q, want sanitized forbidden code", body)
			}
		})
	}
}

func TestBookEventsSanitizesUnsupportedDeadlineFailure(t *testing.T) {
	principal := authflow.Principal{
		UserID:    "reader-1",
		SessionID: "session-1",
		Role:      "reader",
		Status:    "active",
	}
	hub := NewBookStatusHub(1)
	hub.SetAvailable(true)
	handler := &BooksHandler{events: &bookEvents{
		sessions: sseSessionStub{principal: principal},
		hub:      hub,
	}}
	request := httptest.NewRequest(http.MethodGet, "/books/events", nil)
	request = request.WithContext(edgemiddleware.WithClaims(request.Context(), auth.Claims{
		UserID:    principal.UserID,
		SessionID: principal.SessionID,
		Role:      auth.RoleReader,
		ExpiresAt: time.Now().Add(time.Minute),
	}))
	response := httptest.NewRecorder()

	handler.Events(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("SSE response status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"code":"stream_unavailable"`) {
		t.Fatalf("SSE response body = %q, want sanitized stream-unavailable code", body)
	}
	if strings.Contains(strings.ToLower(body), "deadline") || strings.Contains(strings.ToLower(body), "unsupported") {
		t.Fatalf("SSE response body exposed internal failure: %q", body)
	}
}

func TestBookEventsOriginRejectsMissingOrigin(t *testing.T) {
	handler := &bookEvents{
		publicOrigin:  "https://app.example",
		enforceOrigin: true,
	}
	request := httptest.NewRequest(http.MethodGet, "/books/events", nil)

	if handler.originAllowed(request) {
		t.Fatal("stream without Origin was allowed")
	}
}

func TestBookEventsOriginRejectsCrossSiteFetchWithoutOrigin(t *testing.T) {
	handler := &bookEvents{
		publicOrigin:  "https://app.example",
		enforceOrigin: true,
	}
	request := httptest.NewRequest(http.MethodGet, "/books/events", nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")

	if handler.originAllowed(request) {
		t.Fatal("cross-site GET stream without Origin was allowed")
	}
}

func TestBookEventsOriginRejectsMismatchedOrigin(t *testing.T) {
	handler := &bookEvents{
		publicOrigin:  "https://app.example",
		enforceOrigin: true,
	}
	request := httptest.NewRequest(http.MethodGet, "/books/events", nil)
	request.Header.Set("Origin", "https://evil.example")
	request.Header.Set("Sec-Fetch-Site", "same-origin")

	if handler.originAllowed(request) {
		t.Fatal("mismatched Origin was allowed")
	}
}

func testSSETiming() sseTiming {
	return sseTiming{
		heartbeatInterval:  testSSEHeartbeat,
		revalidateInterval: time.Second,
		maximumDuration:    time.Second,
		writeTimeout:       testSSEWriteTimeout,
	}
}

func assertSSEHeartbeatsSurviveWriteTimeout(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.Config.WriteTimeout = testSSEWriteTimeout
	server.Start()
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("SSE response status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	scanner := bufio.NewScanner(response.Body)
	heartbeats := 0
	for scanner.Scan() {
		if scanner.Text() == ": heartbeat" {
			heartbeats++
			if heartbeats == 2 {
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read SSE stream after %d heartbeats: %v", heartbeats, err)
	}
	t.Fatalf("SSE stream ended after %d heartbeats, want at least 2", heartbeats)
}
