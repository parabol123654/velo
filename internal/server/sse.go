package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type Event struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
	// projectID scopes the event for project-restricted API tokens; "" means
	// not tied to a project (user events). Not sent to clients.
	projectID string
}

// hiddenProject marks an event whose project could not be determined: only
// unrestricted clients receive it.
const hiddenProject = "?"

type sseClient struct {
	userID   string
	projects []string // nil: every project
}

func (c sseClient) wants(ev Event) bool {
	if c.projects == nil || ev.projectID == "" {
		return true
	}
	for _, id := range c.projects {
		if id == ev.projectID {
			return true
		}
	}
	return false
}

type SSEHub struct {
	mu      sync.Mutex
	clients map[chan Event]sseClient
}

func NewSSEHub() *SSEHub {
	return &SSEHub{
		clients: make(map[chan Event]sseClient),
	}
}

// Subscribe registers a stream; projects limits it to those project IDs (nil: all).
func (h *SSEHub) Subscribe(userID string, projects []string) (chan Event, func()) {
	ch := make(chan Event, 32)
	h.mu.Lock()
	h.clients[ch] = sseClient{userID: userID, projects: projects}
	h.mu.Unlock()
	return ch, func() { h.remove(ch) }
}

// remove closes ch once; safe to call again after DisconnectUser closed it.
func (h *SSEHub) remove(ch chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
}

// DisconnectUser ends every open stream of a user (disabled, deleted or
// password reset), so they stop receiving events without waiting to reconnect.
func (h *SSEHub) DisconnectUser(userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, c := range h.clients {
		if c.userID == userID {
			delete(h.clients, ch)
			close(ch)
		}
	}
}

// Broadcast sends an event to every stream allowed to see projectID.
func (h *SSEHub) Broadcast(eventType, projectID string, payload any) {
	ev := Event{Type: eventType, Payload: payload, projectID: projectID}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, c := range h.clients {
		if !c.wants(ev) {
			continue
		}
		select {
		case ch <- ev:
		default:
			// Drop if slow client buffer is full; client can re-sync.
		}
	}
}

func (h *SSEHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	var userID string
	if u := currentUser(r); u != nil {
		userID = u.ID
	}
	ch, unsub := h.Subscribe(userID, allowedProjects(r))
	defer unsub()

	fmt.Fprintf(w, "event: connected\ndata: {\"ok\":true}\n\n")
	flusher.Flush()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			flusher.Flush()
		}
	}
}
