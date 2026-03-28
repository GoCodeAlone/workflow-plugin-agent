package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// pendingInteraction tracks a single interaction awaiting a response.
type pendingInteraction struct {
	Interaction Interaction
	responseCh  chan InteractionResponse
}

// HTTPSource exposes pending interactions via an API so that humans or QA
// scripts can act as the LLM. When the agent calls Chat(), the interaction
// is stored as pending and an SSE event is broadcast. A subsequent API call
// provides the response, unblocking the waiting goroutine.
type HTTPSource struct {
	pending map[string]*pendingInteraction
	mu      sync.Mutex
	sseHub  *SSEHub
}

// NewHTTPSource creates an HTTPSource. The optional SSEHub is used to push
// notifications when new interactions arrive.
func NewHTTPSource(hub *SSEHub) *HTTPSource {
	return &HTTPSource{
		pending: make(map[string]*pendingInteraction),
		sseHub:  hub,
	}
}

// SetSSEHub sets or replaces the SSE hub for push notifications.
func (h *HTTPSource) SetSSEHub(hub *SSEHub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sseHub = hub
}

// GetResponse implements ResponseSource.
// It adds the interaction to the pending map, broadcasts an SSE event,
// and blocks until a response is submitted via Respond() or the context
// is cancelled.
func (h *HTTPSource) GetResponse(ctx context.Context, interaction Interaction) (*InteractionResponse, error) {
	responseCh := make(chan InteractionResponse, 1)

	h.mu.Lock()
	h.pending[interaction.ID] = &pendingInteraction{
		Interaction: interaction,
		responseCh:  responseCh,
	}
	hub := h.sseHub
	h.mu.Unlock()

	// Notify via SSE
	if hub != nil {
		eventData, _ := json.Marshal(map[string]any{
			"id":         interaction.ID,
			"tool_count": len(interaction.Tools),
			"msg_count":  len(interaction.Messages),
			"created_at": interaction.CreatedAt.Format(time.RFC3339),
		})
		hub.BroadcastEvent("test_interaction_pending", string(eventData))
	}

	// Wait for response
	select {
	case resp := <-responseCh:
		return &resp, nil
	case <-ctx.Done():
		// Clean up on cancellation
		h.mu.Lock()
		delete(h.pending, interaction.ID)
		h.mu.Unlock()
		return nil, fmt.Errorf("http source: context cancelled: %w", ctx.Err())
	}
}

// InteractionSummary is a brief view of a pending interaction for list endpoints.
type InteractionSummary struct {
	ID        string    `json:"id"`
	MsgCount  int       `json:"msg_count"`
	ToolCount int       `json:"tool_count"`
	CreatedAt time.Time `json:"created_at"`
}

// ListPending returns summaries of all pending interactions.
func (h *HTTPSource) ListPending() []InteractionSummary {
	h.mu.Lock()
	defer h.mu.Unlock()
	summaries := make([]InteractionSummary, 0, len(h.pending))
	for _, pi := range h.pending {
		summaries = append(summaries, InteractionSummary{
			ID:        pi.Interaction.ID,
			MsgCount:  len(pi.Interaction.Messages),
			ToolCount: len(pi.Interaction.Tools),
			CreatedAt: pi.Interaction.CreatedAt,
		})
	}
	return summaries
}

// GetInteraction returns the full interaction details for a given ID.
func (h *HTTPSource) GetInteraction(id string) (*Interaction, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pi, ok := h.pending[id]
	if !ok {
		return nil, fmt.Errorf("interaction %q not found", id)
	}
	return &pi.Interaction, nil
}

// Respond submits a response for a pending interaction, unblocking the
// waiting GetResponse() call.
func (h *HTTPSource) Respond(id string, resp InteractionResponse) error {
	h.mu.Lock()
	pi, ok := h.pending[id]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("interaction %q not found or already responded", id)
	}
	delete(h.pending, id)
	h.mu.Unlock()

	pi.responseCh <- resp
	return nil
}

// PendingCount returns the number of interactions awaiting responses.
func (h *HTTPSource) PendingCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pending)
}
