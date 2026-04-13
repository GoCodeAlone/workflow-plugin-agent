package orchestrator

import (
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/safety"
)

func TestGuardrails_FilterTools(t *testing.T) {
	g := &GuardrailsModule{
		name: "guardrails",
		defaults: GuardrailsDefaults{
			AllowedTools: []string{"file_read", "file_write", "mcp_wfctl__*"},
		},
		analyzer: safety.NewCommandAnalyzer(safety.DefaultPolicy()),
	}

	allTools := []provider.ToolDef{
		{Name: "file_read"},
		{Name: "file_write"},
		{Name: "shell_exec"},
		{Name: "mcp_wfctl__validate_config"},
		{Name: "mcp_wfctl__inspect_config"},
		{Name: "git_commit"},
		{Name: "google_search"},
	}

	filtered := g.FilterTools(allTools)
	names := make([]string, len(filtered))
	for i, t := range filtered {
		names[i] = t.Name
	}

	// Should include: file_read, file_write, mcp_wfctl__validate_config, mcp_wfctl__inspect_config
	// Should exclude: shell_exec, git_commit, google_search
	if len(filtered) != 4 {
		t.Errorf("expected 4 tools, got %d: %v", len(filtered), names)
	}
}

func TestGuardrails_FilterTools_BlockedWinsOverAllowStar(t *testing.T) {
	// allow-all + block-specific: blocked tools must not appear in filtered set.
	g := &GuardrailsModule{
		name: "guardrails",
		defaults: GuardrailsDefaults{
			AllowedTools: []string{"*"},
			BlockedTools: []string{"shell_exec", "git_push"},
		},
		analyzer: safety.NewCommandAnalyzer(safety.DefaultPolicy()),
	}

	tools := []provider.ToolDef{
		{Name: "file_read"},
		{Name: "shell_exec"},
		{Name: "git_push"},
		{Name: "file_write"},
	}

	filtered := g.FilterTools(tools)
	for _, tool := range filtered {
		if tool.Name == "shell_exec" || tool.Name == "git_push" {
			t.Errorf("blocked tool %q should not appear in filtered list", tool.Name)
		}
	}
	if len(filtered) != 2 {
		names := make([]string, len(filtered))
		for i, x := range filtered {
			names[i] = x.Name
		}
		t.Errorf("expected 2 tools (file_read, file_write), got %d: %v", len(filtered), names)
	}
}

func TestGuardrails_FilterTools_NoAllowList(t *testing.T) {
	g := &GuardrailsModule{
		name:     "guardrails",
		defaults: GuardrailsDefaults{},
		analyzer: safety.NewCommandAnalyzer(safety.DefaultPolicy()),
	}

	tools := []provider.ToolDef{
		{Name: "file_read"},
		{Name: "shell_exec"},
		{Name: "git_commit"},
	}

	filtered := g.FilterTools(tools)
	if len(filtered) != len(tools) {
		t.Errorf("no allowlist: expected all %d tools, got %d", len(tools), len(filtered))
	}
}

func TestGuardrails_FilterTools_NilGuardrails(t *testing.T) {
	var g *GuardrailsModule

	tools := []provider.ToolDef{{Name: "file_read"}, {Name: "shell_exec"}}
	filtered := g.FilterTools(tools)
	if len(filtered) != len(tools) {
		t.Errorf("nil guardrails: expected all tools pass through")
	}
}

func TestGuardrails_FilterTools_GlobPattern(t *testing.T) {
	g := &GuardrailsModule{
		name: "guardrails",
		defaults: GuardrailsDefaults{
			AllowedTools: []string{"mcp_*"},
		},
		analyzer: safety.NewCommandAnalyzer(safety.DefaultPolicy()),
	}

	tools := []provider.ToolDef{
		{Name: "mcp_foo"},
		{Name: "mcp_bar_baz"},
		{Name: "not_mcp"},
		{Name: "file_read"},
	}

	filtered := g.FilterTools(tools)
	if len(filtered) != 2 {
		names := make([]string, len(filtered))
		for i, t := range filtered {
			names[i] = t.Name
		}
		t.Errorf("expected 2 mcp_* tools, got %d: %v", len(filtered), names)
	}
}
