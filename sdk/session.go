// Package sdk provides reusable daemon-client infrastructure for agent CLI tools.
// It is game-agnostic; all game-specific behavior is injected via callbacks.
package sdk

import (
	"encoding/json"
	"os"
)

// SessionState is persisted to disk between daemon-mode invocations so that
// reconnecting clients can resume their identity and active game/room.
type SessionState struct {
	GameID           string         `json:"gameId"`
	LastConnectionID string         `json:"lastConnectionId"`
	Token            string         `json:"token,omitempty"`
	Custom           map[string]any `json:"custom,omitempty"` // app-specific state
}

// LoadSession reads a session file and returns the state.
// Returns a zero value if the file is missing or unreadable.
func LoadSession(path string) SessionState {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionState{}
	}
	var s SessionState
	json.Unmarshal(data, &s) //nolint:errcheck // best-effort; zero value on parse failure is fine
	return s
}

// SaveSession writes the session state to path, creating or overwriting the file.
// The file is written with mode 0600 (owner read/write only).
func SaveSession(path string, s SessionState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
