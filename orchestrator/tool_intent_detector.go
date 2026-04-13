package orchestrator

import (
	"strings"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// containsToolIntent returns true when the LLM response describes the intent
// to call a tool in prose without actually emitting a tool call. This catches
// patterns like "I'll call file_write" or "let me use the search tool".
func containsToolIntent(content string, toolDefs []provider.ToolDef) bool {
	lower := strings.ToLower(content)

	intentPatterns := []string{
		"let's call", "i'll call", "i will call", "let me call",
		"i'll use", "i will use", "let me use", "let's use",
		"now i'll", "now let's", "next step is to call",
	}
	for _, p := range intentPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	// Check if any registered tool name appears in prose.
	for _, td := range toolDefs {
		if strings.Contains(lower, strings.ToLower(td.Name)) {
			return true
		}
	}
	return false
}
