package handler

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	edgemiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

// SetupUseCase defines the Identity operations exposed during initial setup.
type SetupUseCase interface {
	SetupStatus(context.Context) (bool, error)
	BootstrapAdmin(context.Context, string, string, string, string) error
}

// SetupHandler serves the bounded, one-time administrator setup flow.
type SetupHandler struct{ identity SetupUseCase }

// NewSetupHandler constructs a setup handler with its required Identity port.
func NewSetupHandler(identity SetupUseCase) *SetupHandler {
	if identity == nil {
		panic("handler: setup dependency is required")
	}
	return &SetupHandler{identity: identity}
}

// Status reports whether initial administrator setup is still required.
func (h *SetupHandler) Status(w http.ResponseWriter, r *http.Request) {
	required, err := h.identity.SetupStatus(r.Context())
	if err != nil {
		writeIdentityError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity service unavailable")
		return
	}
	setPrivateNoStore(w)
	writeAuthJSON(w, http.StatusOK, struct {
		Required bool `json:"required"`
	}{Required: required})
}

// CreateAdmin attempts the one-time bootstrap administrator creation.
func (h *SetupHandler) CreateAdmin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name          string `json:"name"`
		Email         string `json:"email"`
		Password      string `json:"password"`
		BootstrapCode string `json:"bootstrap_code"`
	}
	if err := decodeJSONBody(w, r, &request); err != nil {
		writeIdentityError(w, r, http.StatusBadRequest, "invalid_request", "invalid setup request")
		return
	}
	err := h.identity.BootstrapAdmin(r.Context(), request.Name, request.Email, request.Password, request.BootstrapCode)
	if err != nil {
		switch {
		case errors.Is(err, authflow.ErrConflict):
			writeIdentityError(w, r, http.StatusConflict, "setup_complete", "setup is unavailable")
		case errors.Is(err, authflow.ErrForbidden):
			writeIdentityError(w, r, http.StatusUnauthorized, "setup_unavailable", "setup is unavailable")
		case errors.Is(err, authflow.ErrInvalidRegistration):
			writeIdentityError(w, r, http.StatusBadRequest, "invalid_setup", "invalid setup request")
		default:
			writeIdentityError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity service unavailable")
		}
		return
	}
	setPrivateNoStore(w)
	writeAuthJSON(w, http.StatusCreated, struct {
		Created bool `json:"created"`
	}{Created: true})
}

// AdminUseCase defines administrator review and session-validation operations.
type AdminUseCase interface {
	ListPending(context.Context, authflow.Principal, int, string) (authflow.PendingPage, error)
	Approve(context.Context, authflow.Principal, string) error
	Reject(context.Context, authflow.Principal, string) error
	ValidateSession(context.Context, string, string) (authflow.Principal, error)
}

// PendingHub fans out bounded change notifications to administrator streams.
type PendingHub struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]subscription
	bySession   map[string]int
	byIP        map[string]int
	limit       int
}

type subscription struct{ sessionID, ip string }

// NewPendingHub constructs a hub with a process-wide subscriber limit.
func NewPendingHub(limit int) *PendingHub {
	if limit < 1 {
		panic("handler: positive SSE limit is required")
	}
	return &PendingHub{subscribers: make(map[chan struct{}]subscription), bySession: make(map[string]int), byIP: make(map[string]int), limit: limit}
}

// Publish non-blockingly notifies every current subscriber of a pending change.
func (h *PendingHub) Publish() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for subscriber := range h.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
}

func (h *PendingHub) subscribe(sessionID, ip string) (<-chan struct{}, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subscribers) >= h.limit || h.bySession[sessionID] >= 1 || h.byIP[ip] >= 10 {
		return nil, nil, false
	}
	channel := make(chan struct{}, 1)
	h.subscribers[channel] = subscription{sessionID: sessionID, ip: ip}
	h.bySession[sessionID]++
	h.byIP[ip]++
	remove := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if subscription, ok := h.subscribers[channel]; ok {
			delete(h.subscribers, channel)
			h.bySession[subscription.sessionID]--
			h.byIP[subscription.ip]--
			close(channel)
		}
	}
	return channel, remove, true
}

// AdminHandler serves administrator librarian-review routes and notifications.
type AdminHandler struct {
	identity AdminUseCase
	hub      *PendingHub
	timing   sseTiming
}

// NewAdminHandler constructs an administrator handler with required dependencies.
func NewAdminHandler(identity AdminUseCase, hub *PendingHub) *AdminHandler {
	if identity == nil || hub == nil {
		panic("handler: admin dependencies are required")
	}
	return &AdminHandler{identity: identity, hub: hub}
}

// ListPending returns one bounded page of librarians awaiting review.
func (h *AdminHandler) ListPending(w http.ResponseWriter, r *http.Request) {
	principal, ok := edgemiddleware.PrincipalFromContext(r.Context())
	if !ok || !principal.IsActiveAdmin() {
		writeIdentityError(w, r, http.StatusForbidden, "forbidden", "forbidden")
		return
	}
	pageSize := 25
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeIdentityError(w, r, http.StatusBadRequest, "invalid_page", "invalid page")
			return
		}
		pageSize = value
	}
	page, err := h.identity.ListPending(r.Context(), principal, pageSize, r.URL.Query().Get("page_token"))
	if err != nil {
		h.writeAdminError(w, r, err)
		return
	}
	type item struct {
		UserID       string `json:"user_id"`
		Name         string `json:"name"`
		Email        string `json:"email"`
		RegisteredAt string `json:"registered_at"`
	}
	response := struct {
		Users         []item `json:"users"`
		NextPageToken string `json:"next_page_token,omitempty"`
	}{Users: make([]item, 0, len(page.Users)), NextPageToken: page.NextPageToken}
	for _, user := range page.Users {
		response.Users = append(response.Users, item{UserID: user.UserID, Name: user.Name, Email: user.Email, RegisteredAt: user.RegisteredAt})
	}
	setPrivateNoStore(w)
	writeAuthJSON(w, http.StatusOK, response)
}

// Approve activates a pending librarian after an authoritative admin check.
func (h *AdminHandler) Approve(w http.ResponseWriter, r *http.Request) { h.decide(w, r, true) }

// Reject rejects a pending librarian after an authoritative admin check.
func (h *AdminHandler) Reject(w http.ResponseWriter, r *http.Request) { h.decide(w, r, false) }

func (h *AdminHandler) decide(w http.ResponseWriter, r *http.Request, approve bool) {
	principal, ok := edgemiddleware.PrincipalFromContext(r.Context())
	if !ok || !principal.IsActiveAdmin() {
		writeIdentityError(w, r, http.StatusForbidden, "forbidden", "forbidden")
		return
	}
	var request struct {
		UserID string `json:"user_id"`
	}
	if err := decodeJSONBody(w, r, &request); err != nil || request.UserID == "" {
		writeIdentityError(w, r, http.StatusBadRequest, "invalid_request", "invalid decision")
		return
	}
	var err error
	if approve {
		err = h.identity.Approve(r.Context(), principal, request.UserID)
	} else {
		err = h.identity.Reject(r.Context(), principal, request.UserID)
	}
	if err != nil {
		h.writeAdminError(w, r, err)
		return
	}
	setPrivateNoStore(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandler) writeAdminError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, authflow.ErrForbidden):
		writeIdentityError(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, authflow.ErrInvalidRegistration):
		writeIdentityError(w, r, http.StatusBadRequest, "invalid_request", "invalid request")
	case errors.Is(err, authflow.ErrConflict):
		writeIdentityError(w, r, http.StatusConflict, "state_conflict", "state conflict")
	default:
		writeIdentityError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity service unavailable")
	}
}

// Events streams bounded pending-librarian change notifications to an admin.
func (h *AdminHandler) Events(w http.ResponseWriter, r *http.Request) {
	principal, ok := edgemiddleware.PrincipalFromContext(r.Context())
	if !ok || !principal.IsActiveAdmin() {
		writeIdentityError(w, r, http.StatusForbidden, "forbidden", "forbidden")
		return
	}
	claims, ok := edgemiddleware.ClaimsFromContext(r.Context())
	if !ok || claims.ExpiresAt.IsZero() {
		writeIdentityError(w, r, http.StatusForbidden, "forbidden", "forbidden")
		return
	}
	timing := h.timing.withDefaults()
	streamDuration := time.Until(claims.ExpiresAt)
	if streamDuration <= 0 {
		writeIdentityError(w, r, http.StatusForbidden, "forbidden", "forbidden")
		return
	}
	if streamDuration > timing.maximumDuration {
		streamDuration = timing.maximumDuration
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = "unknown"
	}
	events, remove, ok := h.hub.subscribe(principal.SessionID, ip)
	if !ok {
		writeIdentityError(w, r, http.StatusTooManyRequests, "stream_limit", "stream unavailable")
		return
	}
	defer remove()
	stream, err := newSSEWriter(w, timing)
	if err != nil {
		writeIdentityError(w, r, http.StatusInternalServerError, "stream_unavailable", "stream unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err = stream.flushHeaders(); err != nil {
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
			current, validateErr := h.identity.ValidateSession(r.Context(), principal.UserID, principal.SessionID)
			if validateErr != nil || !current.IsActiveAdmin() {
				return
			}
		case <-heartbeat.C:
			if err = stream.writeFrame([]byte(": heartbeat\n\n")); err != nil {
				return
			}
		case _, open := <-events:
			if !open {
				return
			}
			if err = stream.writeFrame([]byte("event: pending-librarians-changed\ndata: {\"version\":1}\n\n")); err != nil {
				return
			}
		}
	}
}
