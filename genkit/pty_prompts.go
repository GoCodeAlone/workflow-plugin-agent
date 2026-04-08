package genkit

import (
	"regexp"
	"strings"

	"github.com/GoCodeAlone/workflow-plugin-agent/policy"
)

// PromptAction is what the handler decides to do with a detected screen prompt.
type PromptAction int

const (
	PromptActionNone    PromptAction = iota // no prompt detected
	PromptActionRespond                     // auto-respond with a keystroke
	PromptActionQueue                       // queue for human approval
)

// PromptPattern is a screen content pattern with a default action.
type PromptPattern struct {
	Name    string
	MatchRe string              // regex string to match against screen content
	Extract func(string) string // optional: extract the action/path from screen
	Default policy.Action       // what to do if no trust rule matches
	re      *regexp.Regexp      // compiled regex (lazy)
}

// PromptHandler auto-responds to known screen prompts from CLI tools.
type PromptHandler struct {
	trust    *policy.TrustEngine
	patterns []PromptPattern
	onQueue  func(agentName, promptText string)
}

// Built-in patterns for common CLI prompts.
var builtinPromptPatterns = []PromptPattern{
	{
		Name:    "trust_dialog",
		MatchRe: `(?i)(trust this folder|safety check|Confirm folder|Do you trust)`,
		Default: policy.Allow,
	},
	{
		Name:    "command_exec",
		MatchRe: `(?i)(Run command|execute|allow.*command).*\?.*\(y/n\)`,
		Extract: extractCommand,
		Default: policy.Ask,
	},
	{
		Name:    "file_write",
		MatchRe: `(?i)(allow (write|edit|create)|write to|create file).*\?.*\(y/n\)`,
		Extract: extractPath,
		Default: policy.Ask,
	},
	{
		Name:    "permission_prompt",
		MatchRe: `(?i)(allow|approve|permit).*\?.*\(y/n\)`,
		Default: policy.Ask,
	},
}

// NewPromptHandler creates a PromptHandler with the given trust engine.
// customPatterns are checked before built-in patterns.
// onQueue is called when a prompt requires human approval.
func NewPromptHandler(trust *policy.TrustEngine, customPatterns []PromptPattern, onQueue func(agentName, promptText string)) *PromptHandler {
	patterns := make([]PromptPattern, 0, len(customPatterns)+len(builtinPromptPatterns))
	patterns = append(patterns, customPatterns...)
	patterns = append(patterns, builtinPromptPatterns...)

	// Compile regexes
	for i := range patterns {
		if patterns[i].re == nil && patterns[i].MatchRe != "" {
			patterns[i].re = regexp.MustCompile(patterns[i].MatchRe)
		}
	}

	return &PromptHandler{
		trust:    trust,
		patterns: patterns,
		onQueue:  onQueue,
	}
}

// Evaluate checks the screen content against known patterns.
// Returns the action to take and the response string to send (if PromptActionRespond).
func (ph *PromptHandler) Evaluate(screen string) (PromptAction, string) {
	clean := stripANSI(screen)

	for _, p := range ph.patterns {
		if p.re == nil {
			continue
		}
		if !p.re.MatchString(clean) {
			continue
		}

		// Trust dialog: always auto-approve with Enter.
		if p.Name == "trust_dialog" {
			return PromptActionRespond, "\r"
		}

		// Extract the specific action/command if possible.
		var extracted string
		if p.Extract != nil {
			extracted = p.Extract(clean)
		}

		// Evaluate against trust engine.
		action := p.Default
		if ph.trust != nil && extracted != "" {
			if p.Name == "command_exec" {
				// Commands: trust engine result is authoritative (Deny → n\r, Allow → y\r).
				action = ph.trust.EvaluateCommand(extracted)
			} else if p.Name == "file_write" {
				// Paths: only upgrade to Allow when explicitly permitted.
				// EvaluatePath returns Deny both for explicit denies and no-match (default),
				// so we fall back to p.Default (Ask) for anything that isn't an explicit Allow.
				if ph.trust.EvaluatePath(extracted) == policy.Allow {
					action = policy.Allow
				}
			}
		}

		switch action {
		case policy.Allow:
			return PromptActionRespond, "y\r"
		case policy.Deny:
			return PromptActionRespond, "n\r"
		case policy.Ask:
			if ph.onQueue != nil {
				ph.onQueue("", clean)
			}
			return PromptActionQueue, ""
		}
	}

	return PromptActionNone, ""
}

// extractCommand attempts to extract the command from a "Run command: xxx?" prompt.
var commandExtractRe = regexp.MustCompile(`(?i)(?:Run command|execute)[:\s]+([^\?]+)`)

func extractCommand(screen string) string {
	m := commandExtractRe.FindStringSubmatch(screen)
	if len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// extractPath attempts to extract a file path from a permission prompt.
var pathExtractRe = regexp.MustCompile(`(?:write to|edit|create file|allow write)[:\s]+([/~][^\?\s]+)`)

func extractPath(screen string) string {
	m := pathExtractRe.FindStringSubmatch(screen)
	if len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}
