// Package genkit provides Genkit-backed implementations of provider.Provider.
package genkit

import (
	"context"
	"sync"

	gk "github.com/firebase/genkit/go/genkit"
)

var (
	instance *gk.Genkit
	once     sync.Once
)

// Instance returns the shared Genkit instance, initializing it lazily on first call.
// This instance has no plugins and is used for mock/test model definitions.
// Production providers use per-factory Genkit instances initialized with their specific plugin.
func Instance(ctx context.Context) *gk.Genkit {
	once.Do(func() {
		instance = gk.Init(ctx)
	})
	return instance
}
