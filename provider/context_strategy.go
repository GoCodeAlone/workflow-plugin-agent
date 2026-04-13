package provider

import "context"

// ContextStrategy is an optional interface that providers can implement to
// declare they manage conversation context server-side. When implemented,
// the executor sends only new messages since the last call instead of the
// full history.
type ContextStrategy interface {
	// ManagesContext returns true if the provider maintains conversation
	// state between Chat() calls.
	ManagesContext() bool

	// ResetContext clears any accumulated server-side state.
	// Called when the executor compacts context or starts a new session.
	ResetContext(ctx context.Context) error
}
