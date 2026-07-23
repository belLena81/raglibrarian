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
	LifecycleVersion          int64     `json:"lifecycle_version"`
	CanReindex                bool      `json:"can_reindex"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

// BookStatusHub bounds and fans out latest-only status notifications.
type BookStatusHub struct {
	mu          sync.Mutex
	subscribers map[*bookStatusSubscriber]subscription
	bySession   map[string]int
	byIP        map[string]int
	limit       int
	available   bool
}

const maxPendingBookStatusEvents = 64

// bookStatusSubscriber stores its pending state under BookStatusHub.mu. wake is
// only a notification: the SSE writer drains the state from the hub before it
// writes frames, so a slow writer cannot make Publish block.
type bookStatusSubscriber struct {
	wake    chan struct{}
	pending map[string]BookStatusEvent
	resync  bool
}

func NewBookStatusHub(limit int) *BookStatusHub {
	if limit < 1 {
		panic("handler: positive book SSE limit is required")
	}
	return &BookStatusHub{
		subscribers: make(map[*bookStatusSubscriber]subscription),
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
		releaseBookStatusLimit(h.bySession, details.sessionID)
		releaseBookStatusLimit(h.byIP, details.ip)
		close(subscriber.wake)
	}
}

// Publish retains the newest notification for every book for slow subscribers.
// If a subscriber falls more than maxPendingBookStatusEvents books behind, it
// receives one resync notification instead of an incomplete status snapshot.
func (h *BookStatusHub) Publish(event BookStatusEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.available {
		return
	}
	event.SchemaVersion = 1
	for subscriber := range h.subscribers {
		h.queueBookStatusEvent(subscriber, event)
	}
}

func (h *BookStatusHub) queueBookStatusEvent(subscriber *bookStatusSubscriber, event BookStatusEvent) {
	if subscriber.resync {
		return
	}
	if current, ok := subscriber.pending[event.BookID]; ok {
		if current.ProcessingVersion >= event.ProcessingVersion {
			return
		}
		subscriber.pending[event.BookID] = event
		h.signalBookStatusSubscriber(subscriber)
		return
	}
	if len(subscriber.pending) >= maxPendingBookStatusEvents {
		clear(subscriber.pending)
		subscriber.resync = true
		h.signalBookStatusSubscriber(subscriber)
		return
	}
	subscriber.pending[event.BookID] = event
	h.signalBookStatusSubscriber(subscriber)
}

func (h *BookStatusHub) signalBookStatusSubscriber(subscriber *bookStatusSubscriber) {
	select {
	case subscriber.wake <- struct{}{}:
	default:
	}
}

func (h *BookStatusHub) subscribe(sessionID, ip string) (*bookStatusSubscriber, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.available || len(h.subscribers) >= h.limit || h.bySession[sessionID] >= 1 || h.byIP[ip] >= 10 {
		return nil, nil, false
	}
	subscriber := &bookStatusSubscriber{
		wake:    make(chan struct{}, 1),
		pending: make(map[string]BookStatusEvent),
	}
	h.subscribers[subscriber] = subscription{sessionID: sessionID, ip: ip}
	h.bySession[sessionID]++
	h.byIP[ip]++
	remove := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if details, ok := h.subscribers[subscriber]; ok {
			delete(h.subscribers, subscriber)
			releaseBookStatusLimit(h.bySession, details.sessionID)
			releaseBookStatusLimit(h.byIP, details.ip)
			close(subscriber.wake)
		}
	}
	return subscriber, remove, true
}

func releaseBookStatusLimit(counts map[string]int, key string) {
	remaining := counts[key] - 1
	if remaining <= 0 {
		delete(counts, key)
		return
	}
	counts[key] = remaining
}

func (h *BookStatusHub) drain(subscriber *bookStatusSubscriber) ([]BookStatusEvent, bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subscribers[subscriber]; !ok {
		return nil, false, false
	}
	if subscriber.resync {
		subscriber.resync = false
		return nil, true, true
	}
	events := make([]BookStatusEvent, 0, len(subscriber.pending))
	for _, event := range subscriber.pending {
		events = append(events, event)
	}
	clear(subscriber.pending)
	return events, false, true
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
	timing        sseTiming
}

// Events streams sanitized, revisioned processing changes for the library.
func (h *BooksHandler) Events(w http.ResponseWriter, r *http.Request) {
	if h.events == nil {
		writeBookError(w, r, http.StatusServiceUnavailable, "stream_unavailable", "stream unavailable")
		return
	}
	if !h.events.originAllowed(r) {
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
	timing := h.events.timing.withDefaults()
	streamDuration := timing.maximumDuration
	if !claims.ExpiresAt.IsZero() {
		streamDuration = time.Until(claims.ExpiresAt)
		if streamDuration <= 0 {
			writeBookError(w, r, http.StatusUnauthorized, "unauthorized", "invalid or expired credentials")
			return
		}
		if streamDuration > timing.maximumDuration {
			streamDuration = timing.maximumDuration
		}
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = "unknown"
	}
	subscriber, remove, ok := h.events.hub.subscribe(principal.SessionID, ip)
	if !ok {
		writeBookError(w, r, http.StatusServiceUnavailable, "stream_unavailable", "stream unavailable")
		return
	}
	defer remove()
	stream, err := newSSEWriter(w, timing)
	if err != nil {
		writeBookError(w, r, http.StatusInternalServerError, "stream_unavailable", "stream unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err = stream.writeFrame([]byte("event: books-resync\ndata: {\"version\":1}\n\n")); err != nil {
		return
	}
	heartbeat := time.NewTicker(timing.heartbeatInterval)
	revalidate := time.NewTicker(timing.revalidateInterval)
	maximum := time.NewTimer(streamDuration)
	defer heartbeat.Stop()
	defer revalidate.Stop()
	defer maximum.Stop()
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
			if err = stream.writeFrame([]byte(": heartbeat\n\n")); err != nil {
				return
			}
		case _, open := <-subscriber.wake:
			if !open {
				return
			}
			events, resync, subscribed := h.events.hub.drain(subscriber)
			if !subscribed {
				return
			}
			if resync {
				if err = stream.writeFrame([]byte("event: books-resync\ndata: {\"version\":1,\"reason\":\"subscriber_overflow\"}\n\n")); err != nil {
					return
				}
				continue
			}
			for _, event := range events {
				body, marshalErr := json.Marshal(event)
				if marshalErr != nil {
					return
				}
				frame := append([]byte("id: "+event.EventID+"\nevent: book-processing-status-changed\ndata: "), body...)
				frame = append(frame, '\n', '\n')
				if err = stream.writeFrame(frame); err != nil {
					return
				}
			}
		}
	}
}

func (e *bookEvents) originAllowed(r *http.Request) bool {
	if !e.enforceOrigin {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		return origin == e.publicOrigin
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}
