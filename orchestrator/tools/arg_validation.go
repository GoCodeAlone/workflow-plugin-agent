package tools

import (
	"regexp"
	"strings"
)

// placeholderPatterns are compiled regexes that detect placeholder content in tool args.
var placeholderPatterns = []*regexp.Regexp{
	regexp.MustCompile(`<[a-zA-Z_ ]+>`),   // <improved yaml>, <content here>
	regexp.MustCompile(`\$\{[^}]+\}`),      // ${VARIABLE}
	regexp.MustCompile(`\bTODO\b`),
	regexp.MustCompile(`\bFIXME\b`),
	regexp.MustCompile(`\bPLACEHOLDER\b`),
}

// DetectPlaceholder reports whether content looks like a placeholder rather than real content.
// Returns (true, reason) if a placeholder is detected, (false, "") otherwise.
func DetectPlaceholder(content string) (bool, string) {
	for _, re := range placeholderPatterns {
		if re.MatchString(content) {
			return true, re.String()
		}
	}
	if len(strings.TrimSpace(content)) < 10 {
		return true, "content too short (< 10 chars)"
	}
	return false, ""
}
