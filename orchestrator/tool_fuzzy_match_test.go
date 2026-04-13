package orchestrator

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/plugin"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// stubTool is a minimal plugin.Tool for testing.
type stubTool struct{ name string }

func (s *stubTool) Name() string                                              { return s.name }
func (s *stubTool) Description() string                                       { return "" }
func (s *stubTool) Definition() provider.ToolDef                              { return provider.ToolDef{Name: s.name} }
func (s *stubTool) Execute(_ context.Context, _ map[string]any) (any, error) { return nil, nil }

func makeRegistry(names ...string) map[string]plugin.Tool {
	m := make(map[string]plugin.Tool, len(names))
	for _, n := range names {
		m[n] = &stubTool{name: n}
	}
	return m
}

func TestSuggestTool_SubstringMatch(t *testing.T) {
	reg := makeRegistry("file_read", "file_write", "mcp_wfctl__validate_config")
	got := suggestTool("file_manager:read", reg)
	if got != "file_read" {
		t.Errorf("expected file_read, got %q", got)
	}
}

func TestSuggestTool_MCPNameMatch(t *testing.T) {
	reg := makeRegistry("file_read", "mcp_wfctl__validate_config", "mcp_wfctl__inspect_config")
	got := suggestTool("mcp_wfctl_validate", reg)
	if got != "mcp_wfctl__validate_config" {
		t.Errorf("expected mcp_wfctl__validate_config, got %q", got)
	}
}

func TestSuggestTool_LevenshteinFallback(t *testing.T) {
	reg := makeRegistry("file_read", "file_write")
	// "flie_read" is 2 edits from "file_read"
	got := suggestTool("flie_read", reg)
	if got != "file_read" {
		t.Errorf("expected file_read, got %q", got)
	}
}

func TestSuggestTool_NoMatch(t *testing.T) {
	reg := makeRegistry("file_read")
	got := suggestTool("completely_unknown_xyz_tool", reg)
	if got != "" {
		t.Errorf("expected empty suggestion, got %q", got)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"file_read", "file_read", 0},
		{"flie_read", "file_read", 2},
	}
	for _, c := range cases {
		got := levenshtein(c.a, c.b)
		if got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
