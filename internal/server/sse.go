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
}

type SSEHub struct {
	mu      sync.Mutex
	clients map[chan Event]string // channel -> user ID
}

func NewSSEHub() *SSEHub {
	return &SSEHub{
		clients: make(map[chan Event]string),
	}
}

func (h *SSEHub) Subscribe(userID string) (chan Event, func()) {
	ch := make(chan Event, 32)
	h.mu.Lock()
	h.clients[ch] = userID
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
	for ch, uid := range h.clients {
		if uid == userID {
			delete(h.clients, ch)
			close(ch)
		}
	}
}

func (h *SSEHub) Broadcast(eventType string, payload any) {
	ev := Event{Type: eventType, Payload: payload}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
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
	ch, unsub := h.Subscribe(userID)
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
