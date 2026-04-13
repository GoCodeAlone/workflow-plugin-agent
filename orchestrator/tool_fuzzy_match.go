package orchestrator

import (
	"strings"

	"github.com/GoCodeAlone/workflow-plugin-agent/plugin"
)

// suggestTool finds the closest tool name in the registry to the given name.
// It tries several strategies in order: exact prefix match (after normalizing
// double-underscores), base-name substring match, and finally Levenshtein distance.
// Returns empty string if no good match found.
func suggestTool(name string, registry map[string]plugin.Tool) string {
	baseName := extractBaseName(name)
	// Normalize the input by collapsing __ → _ for MCP-style name comparisons.
	normName := strings.ReplaceAll(name, "__", "_")

	for regName := range registry {
		regBase := extractBaseName(regName)
		normReg := strings.ReplaceAll(regName, "__", "_")

		// Prefix match on normalized names (catches "mcp_wfctl_validate" → "mcp_wfctl__validate_config")
		if strings.HasPrefix(normReg, normName) || strings.HasPrefix(normName, normReg) {
			return regName
		}
		// Base-name substring match (catches "file_manager:read" → "file_read")
		if strings.Contains(regName, baseName) || strings.Contains(name, regBase) {
			return regName
		}
	}

	// Levenshtein as fallback
	bestMatch := ""
	bestDist := 999
	for regName := range registry {
		dist := levenshtein(name, regName)
		if dist < bestDist && dist <= 5 {
			bestDist = dist
			bestMatch = regName
		}
	}
	return bestMatch
}

// extractBaseName strips namespace prefixes (e.g. "file_manager:read" → "read",
// "mcp_wfctl__validate_config" → "validate_config").
func extractBaseName(name string) string {
	if i := strings.LastIndex(name, ":"); i >= 0 {
		return name[i+1:]
	}
	if i := strings.LastIndex(name, "__"); i >= 0 {
		return name[i+2:]
	}
	return name
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	row := make([]int, lb+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= la; i++ {
		prev := row[0]
		row[0] = i
		for j := 1; j <= lb; j++ {
			tmp := row[j]
			if ra[i-1] == rb[j-1] {
				row[j] = prev
			} else {
				row[j] = 1 + min3(prev, row[j], row[j-1])
			}
			prev = tmp
		}
	}
	return row[lb]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// toolNames returns a sorted list of tool names for error messages.
func toolNames(registry map[string]plugin.Tool) []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
