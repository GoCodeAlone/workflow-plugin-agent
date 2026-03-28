package orchestrator

import (
	"context"
	"fmt"
)

// ChannelSource delivers interactions via Go channels, enabling test goroutines
// to drive the agent loop interactively from within a Go test.
type ChannelSource struct {
	interactions chan<- Interaction
	responses    <-chan InteractionResponse
}

// NewChannelSource creates a ChannelSource and returns the source along with
// the test-side channels:
//   - interactionsCh receives Interactions from the provider (test reads from this)
//   - responsesCh accepts InteractionResponses from the test (test writes to this)
func NewChannelSource() (source *ChannelSource, interactionsCh <-chan Interaction, responsesCh chan<- InteractionResponse) {
	iCh := make(chan Interaction, 1)
	rCh := make(chan InteractionResponse, 1)
	source = &ChannelSource{
		interactions: iCh,
		responses:    rCh,
	}
	return source, iCh, rCh
}

// GetResponse implements ResponseSource.
// It sends the interaction on the interactions channel and blocks until
// a response arrives on the responses channel or the context is cancelled.
func (cs *ChannelSource) GetResponse(ctx context.Context, interaction Interaction) (*InteractionResponse, error) {
	// Send interaction to the test goroutine
	select {
	case cs.interactions <- interaction:
	case <-ctx.Done():
		return nil, fmt.Errorf("channel source: context cancelled while sending interaction: %w", ctx.Err())
	}

	// Wait for response from the test goroutine
	select {
	case resp, ok := <-cs.responses:
		if !ok {
			return nil, fmt.Errorf("channel source: response channel closed")
		}
		return &resp, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("channel source: context cancelled while waiting for response: %w", ctx.Err())
	}
}
