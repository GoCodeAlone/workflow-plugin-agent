package orchestrator

import (
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

func TestContainsToolIntent_IntentPhrases(t *testing.T) {
	defs := []provider.ToolDef{{Name: "file_write"}, {Name: "file_read"}}

	cases := []struct {
		content string
		want    bool
	}{
		{"I'll call file_write to save the data", true},
		{"let me use the search tool", true},
		{"i will call file_read", true},
		{"let's call file_write", true},
		{"now let's proceed", true},
		{"now i'll do it", true},
		{"next step is to call file_write", true},
		{"TASK COMPLETE — all done", false},
		{"Here is the result you asked for.", false},
		{"The file has been written.", false},
	}

	for _, tc := range cases {
		got := containsToolIntent(tc.content, defs)
		if got != tc.want {
			t.Errorf("containsToolIntent(%q) = %v, want %v", tc.content, got, tc.want)
		}
	}
}

func TestContainsToolIntent_ToolNameInProse(t *testing.T) {
	defs := []provider.ToolDef{{Name: "special_tool_xyz"}}

	if !containsToolIntent("I should use special_tool_xyz here", defs) {
		t.Error("expected tool name in prose to trigger intent detection")
	}
	if containsToolIntent("nothing relevant here", defs) {
		t.Error("expected no intent when neither patterns nor tool names match")
	}
}

func TestContainsToolIntent_EmptyContent(t *testing.T) {
	defs := []provider.ToolDef{{Name: "file_write"}}
	if containsToolIntent("", defs) {
		t.Error("empty content should not trigger intent detection")
	}
}

func TestContainsToolIntent_NoToolDefs(t *testing.T) {
	if containsToolIntent("i'll call something", nil) {
		// Pattern match still fires — this is expected.
		return
	}
	// Either outcome is fine for nil defs; just ensure no panic.
}
