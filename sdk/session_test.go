package sdk_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/sdk"
)

func TestSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")

	// Load from missing file returns zero value.
	s := sdk.LoadSession(path)
	if s.GameID != "" || s.Token != "" || s.LastConnectionID != "" {
		t.Fatalf("expected zero SessionState for missing file, got %+v", s)
	}

	// Save and reload.
	want := sdk.SessionState{
		GameID:           "game-42",
		LastConnectionID: "conn-abc",
		Token:            "tok-xyz",
		Custom: map[string]any{
			"difficulty": "hard",
		},
	}
	if err := sdk.SaveSession(path, want); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got := sdk.LoadSession(path)
	if got.GameID != want.GameID {
		t.Errorf("GameID: want %q, got %q", want.GameID, got.GameID)
	}
	if got.LastConnectionID != want.LastConnectionID {
		t.Errorf("LastConnectionID: want %q, got %q", want.LastConnectionID, got.LastConnectionID)
	}
	if got.Token != want.Token {
		t.Errorf("Token: want %q, got %q", want.Token, got.Token)
	}
	if v, ok := got.Custom["difficulty"]; !ok || v != "hard" {
		t.Errorf("Custom[difficulty]: want %q, got %v", "hard", v)
	}

	// File must be owner-only.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file permissions: want 0600, got %04o", perm)
	}
}

func TestLoadSessionInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Should return zero value without panic.
	s := sdk.LoadSession(path)
	if s.GameID != "" {
		t.Errorf("expected zero GameID for invalid JSON, got %q", s.GameID)
	}
}
