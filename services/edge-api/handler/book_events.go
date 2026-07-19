package handler

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	"github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

// BookStatusEvent is the content-free projection safe to expose to browsers.
type BookStatusEvent struct {
	EventID                   string    `json:"-"`
	SchemaVersion             int       `json:"schema_version"`
	BookID                    string    `json:"book_id"`
	ProcessingStatus          string    `json:"processing_status"`
	ProcessingStage           string    `json:"processing_stage"`
	ProcessingFailureCategory string    `json:"processing_failure_category,omitempty"`
	ProcessingVersion         int64     `json:"processing_version"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

// BookStatusHub bounds and fans out latest-only status notifications.
type BookStatusHub struct {
	mu          sync.Mutex
	subscribers map[chan BookStatusEvent]subscription
	bySession   map[string]int
	byIP        map[string]int
	limit       int
	available   bool
}

func NewBookStatusHub(limit int) *BookStatusHub {
	if limit < 1 {
		panic("handler: positive book SSE limit is required")
	}
	return &BookStatusHub{
		subscribers: make(map[chan BookStatusEvent]subscription),
		bySession:   make(map[string]int),
		byIP:        make(map[string]int),
		limit:       limit,
	}
}

// SetAvailable records broker health. Losing the broker terminates current
// streams so clients can resynchronize instead of silently waiting on stale data.
func (h *BookStatusHub) SetAvailable(available bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.available == available {
		return
	}
	h.available = available
	if available {
		return
	}
	for subscriber, details := range h.subscribers {
		delete(h.subscribers, subscriber)
		h.bySession[details.sessionID]--
		h.byIP[details.ip]--
		close(subscriber)
	}
}

// Publish retains the newest notification for every slow subscriber.
func (h *BookStatusHub) Publish(event BookStatusEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.available {
		return
	}
	event.SchemaVersion = 1
	for subscriber := range h.subscribers {
		select {
		case subscriber <- event:
		default:
			replaceLatestBookStatusEvent(subscriber, event)
		}
	}
}

func replaceLatestBookStatusEvent(subscriber chan BookStatusEvent, event BookStatusEvent) {
	select {
	case <-subscriber:
	default:
	}
	select {
	case subscriber <- event:
	default:
	}
}

func (h *BookStatusHub) subscribe(sessionID, ip string) (<-chan BookStatusEvent, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.available || len(h.subscribers) >= h.limit || h.bySession[sessionID] >= 1 || h.byIP[ip] >= 10 {
		return nil, nil, false
	}
	channel := make(chan BookStatusEvent, 1)
	h.subscribers[channel] = subscription{sessionID: sessionID, ip: ip}
	h.bySession[sessionID]++
	h.byIP[ip]++
	remove := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if details, ok := h.subscribers[channel]; ok {
			delete(h.subscribers, channel)
			h.bySession[details.sessionID]--
			h.byIP[details.ip]--
			close(channel)
		}
	}
	return channel, remove, true
}

type bookEventSessions interface {
	ValidateSession(context.Context, string, string) (authflow.Principal, error)
}

type BookEventsConfig struct {
	Sessions      bookEventSessions
	Hub           *BookStatusHub
	PublicOrigin  string
	EnforceOrigin bool
}

// EnableEvents adds the optional RabbitMQ-backed status stream to this handler.
func (h *BooksHandler) EnableEvents(config BookEventsConfig) {
	if config.Sessions == nil || config.Hub == nil {
		panic("handler: book event dependencies are required")
	}
	h.events = &bookEvents{sessions: config.Sessions, hub: config.Hub, publicOrigin: config.PublicOrigin, enforceOrigin: config.EnforceOrigin}
}

type bookEvents struct {
	sessions      bookEventSessions
	hub           *BookStatusHub
	publicOrigin  string
	enforceOrigin bool
}

// Events streams sanitized, revisioned processing changes for the library.
func (h *BooksHandler) Events(w http.ResponseWriter, r *http.Request) {
	if h.events == nil {
		writeBookError(w, r, http.StatusServiceUnavailable, "stream_unavailable", "stream unavailable")
		return
	}
	if h.events.enforceOrigin && r.Header.Get("Origin") != h.events.publicOrigin {
		writeBookError(w, r, http.StatusForbidden, "origin_forbidden", "origin forbidden")
		return
	}
	principal, ok := middleware.PrincipalFromContext(r.Context())
	if !ok || principal.Status != "active" {
		writeBookError(w, r, http.StatusUnauthorized, "unauthorized", "invalid or expired credentials")
		return
	}
	claims, ok := middleware.ClaimsFromContext(r.Context())
	if !ok {
		writeBookError(w, r, http.StatusUnauthorized, "unauthorized", "invalid or expired credentials")
		return
	}
	streamDuration := 5 * time.Minute
	if !claims.ExpiresAt.IsZero() {
		streamDuration = time.Until(claims.ExpiresAt)
		if streamDuration <= 0 {
			writeBookError(w, r, http.StatusUnauthorized, "unauthorized", "invalid or expired credentials")
			return
		}
		if streamDuration > 5*time.Minute {
			streamDuration = 5 * time.Minute
		}
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = "unknown"
	}
	events, remove, ok := h.events.hub.subscribe(principal.SessionID, ip)
	if !ok {
		writeBookError(w, r, http.StatusServiceUnavailable, "stream_unavailable", "stream unavailable")
		return
	}
	defer remove()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeBookError(w, r, http.StatusInternalServerError, "stream_unavailable", "stream unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err = w.Write([]byte("event: books-resync\ndata: {\"version\":1}\n\n")); err != nil {
		return
	}
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	revalidate := time.NewTicker(15 * time.Second)
	maximum := time.NewTimer(streamDuration)
	defer heartbeat.Stop()
	defer revalidate.Stop()
	defer maximum.Stop()
	controller := http.NewResponseController(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-maximum.C:
			return
		case <-revalidate.C:
			current, validateErr := h.events.sessions.ValidateSession(r.Context(), principal.UserID, principal.SessionID)
			if validateErr != nil || current.Status != "active" || current.Role != principal.Role {
				return
			}
		case <-heartbeat.C:
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err = w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case event, open := <-events:
			if !open {
				return
			}
			body, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err = w.Write([]byte("id: " + event.EventID + "\nevent: book-processing-status-changed\ndata: ")); err != nil {
				return
			}
			if _, err = w.Write(append(body, '\n', '\n')); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
