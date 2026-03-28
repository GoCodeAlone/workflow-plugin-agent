package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/plugin"
)

// SSEHub is a modular.Module that manages Server-Sent Events connections.
// It provides an HTTP handler for SSE clients and a Broadcast method for
// pushing events from pipeline steps or other parts of the system.
type SSEHub struct {
	name    string
	path    string
	clients map[chan []byte]struct{}
	mu      sync.RWMutex
}

// Name implements modular.Module.
func (h *SSEHub) Name() string { return h.name }

// Init registers the hub as a named service.
func (h *SSEHub) Init(app modular.Application) error {
	return app.RegisterService(h.name, h)
}

// ProvidesServices declares the SSE hub service.
func (h *SSEHub) ProvidesServices() []modular.ServiceProvider {
	return []modular.ServiceProvider{
		{
			Name:        h.name,
			Description: "Ratchet SSE hub: " + h.name,
			Instance:    h,
		},
	}
}

// RequiresServices declares no dependencies.
func (h *SSEHub) RequiresServices() []modular.ServiceDependency {
	return nil
}

// Start implements modular.Startable (no-op — the hub starts on first HTTP connection).
func (h *SSEHub) Start(_ context.Context) error { return nil }

// Stop implements modular.Stoppable — closes all connected clients.
func (h *SSEHub) Stop(_ context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		close(ch)
		delete(h.clients, ch)
	}
	return nil
}

// Path returns the configured SSE endpoint path.
func (h *SSEHub) Path() string { return h.path }

// ServeHTTP handles an incoming SSE connection.
// It sets the required headers, registers the client channel, and streams
// events until the client disconnects or the server shuts down.
func (h *SSEHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Disable write deadline for this long-lived SSE connection so the
	// server's global writeTimeout does not abort the stream.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Register client
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		_, exists := h.clients[ch]
		delete(h.clients, ch)
		h.mu.Unlock()
		if exists {
			close(ch)
		}
	}()

	// Send periodic heartbeat comments to keep the connection alive through
	// proxies and prevent idle-connection timeouts.
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	// Stream events
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case msg, open := <-ch:
			if !open {
				return
			}
			// Each SSE event must be terminated by a blank line (\n\n).
			_, _ = fmt.Fprintf(w, "%s\n\n", msg)
			flusher.Flush()
		}
	}
}

// Broadcast sends a raw SSE payload to all connected clients.
func (h *SSEHub) Broadcast(event []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- event:
		default:
			// Drop if client buffer is full
		}
	}
}

// BroadcastEvent formats and broadcasts a named SSE event with data.
func (h *SSEHub) BroadcastEvent(eventType, data string) {
	payload := fmt.Sprintf("event: %s\ndata: %s", eventType, data)
	h.Broadcast([]byte(payload))
}

// newSSEHubFactory returns a plugin.ModuleFactory for "ratchet.sse_hub".
func newSSEHubFactory() plugin.ModuleFactory {
	return func(name string, cfg map[string]any) modular.Module {
		path, _ := cfg["path"].(string)
		if path == "" {
			path = "/events"
		}
		return &SSEHub{
			name:    name,
			path:    path,
			clients: make(map[chan []byte]struct{}),
		}
	}
}
