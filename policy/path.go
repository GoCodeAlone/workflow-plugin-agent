package policy

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// GlobMatcher evaluates file paths against allow/deny glob patterns.
// Deny patterns are checked first (deny wins). Unmatched paths return Ask.
// Supports standard glob wildcards (*) and doublestar (**) for recursive matching.
// Patterns starting with ~/ are expanded to the user's home directory.
type GlobMatcher struct {
	allow []string // expanded allow patterns
	deny  []string // expanded deny patterns
}

// NewGlobMatcher creates a GlobMatcher from allow and deny glob lists.
// Tilde (~) in patterns is expanded to the user's home directory at construction time.
func NewGlobMatcher(allow, deny []string) *GlobMatcher {
	return &GlobMatcher{
		allow: expandPatterns(allow),
		deny:  expandPatterns(deny),
	}
}

// Check returns Allow, Deny, or Ask for the given path.
// Deny patterns are evaluated first so deny wins over allow.
// An unmatched path returns Ask (not Deny), allowing the caller to prompt the user.
func (gm *GlobMatcher) Check(path string) Action {
	path = filepath.Clean(path)

	for _, pattern := range gm.deny {
		if matchGlob(pattern, path) {
			return Deny
		}
	}
	for _, pattern := range gm.allow {
		if matchGlob(pattern, path) {
			return Allow
		}
	}
	return Ask
}

// expandPatterns expands tilde and cleans each pattern.
func expandPatterns(patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	home, _ := os.UserHomeDir()
	out := make([]string, len(patterns))
	for i, p := range patterns {
		if strings.HasPrefix(p, "~/") && home != "" {
			p = home + p[1:]
		}
		out[i] = p
	}
	return out
}

// matchGlob matches a path against a pattern using doublestar for ** support.
func matchGlob(pattern, path string) bool {
	matched, err := doublestar.Match(pattern, path)
	if err != nil {
		// Malformed pattern — fall back to exact match.
		return pattern == path
	}
	return matched
}
