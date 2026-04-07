package genkit

import (
	"os"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

func TestNewACPProviderMissingBinary(t *testing.T) {
	old := os.Getenv("PATH")
	_ = os.Setenv("PATH", t.TempDir())
	defer func() { _ = os.Setenv("PATH", old) }()

	_, err := NewACPProvider("test-agent", "nonexistent-agent-binary", nil, "")
	if err == nil {
		t.Error("expected error for missing binary")
	}
}

func TestNewACPProviderEmptyPath(t *testing.T) {
	_, err := NewACPProvider("test-agent", "", nil, "")
	if err == nil {
		t.Error("expected error for empty binary path")
	}
}

func TestACPProviderName(t *testing.T) {
	// Use a binary that exists on PATH for the constructor, but we won't connect.
	p := &acpProvider{
		name: "test_acp",
		authInfo: provider.AuthModeInfo{
			Mode:        "none",
			DisplayName: "acp:test_acp",
		},
	}
	if p.Name() != "test_acp" {
		t.Errorf("Name() = %q, want %q", p.Name(), "test_acp")
	}
	info := p.AuthModeInfo()
	if info.Mode != "none" {
		t.Errorf("AuthModeInfo().Mode = %q, want %q", info.Mode, "none")
	}
}

func TestACPProviderClose(t *testing.T) {
	p := &acpProvider{name: "test"}
	if err := p.Close(); err != nil {
		t.Errorf("Close() on idle provider: %v", err)
	}
}
