package executor

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// LoopStatus represents the result of a loop check.
type LoopStatus int

const (
	// LoopStatusOK means no loop detected.
	LoopStatusOK LoopStatus = iota
	// LoopStatusWarning means a potential loop pattern is forming.
	LoopStatusWarning
	// LoopStatusBreak means a definitive loop is detected; execution should stop.
	LoopStatusBreak
)

// loopEntry records a single tool invocation for loop analysis.
type loopEntry struct {
	ToolName   string
	ArgsHash   string
	ResultHash string
	IsError    bool
	ErrorMsg   string
}

// LoopDetectorConfig holds configurable thresholds for loop detection.
type LoopDetectorConfig struct {
	// MaxConsecutive is the number of identical consecutive tool calls before a loop is detected.
	// Default: 3.
	MaxConsecutive int
	// MaxErrors is the number of times the same tool call can return the same error before a loop is detected.
	// Default: 2.
	MaxErrors int
	// MaxAlternating is the number of A/B alternating cycles before a loop is detected.
	// Default: 3.
	MaxAlternating int
	// MaxNoProgress is the number of identical (same args + same result) non-error calls before a loop is detected.
	// Default: 3.
	MaxNoProgress int
}

// LoopDetector detects agent execution loops using multiple heuristics.
type LoopDetector struct {
	maxConsecutive int
	maxErrors      int
	maxAlternating int
	maxNoProgress  int
	history        []loopEntry
}

// NewLoopDetector creates a LoopDetector with the given config.
// Zero values in cfg are replaced with defaults (MaxConsecutive=3, MaxErrors=2,
// MaxAlternating=3, MaxNoProgress=3).
func NewLoopDetector(cfg LoopDetectorConfig) *LoopDetector {
	if cfg.MaxConsecutive <= 0 {
		cfg.MaxConsecutive = 3
	}
	if cfg.MaxErrors <= 0 {
		cfg.MaxErrors = 2
	}
	if cfg.MaxAlternating <= 0 {
		cfg.MaxAlternating = 3
	}
	if cfg.MaxNoProgress <= 0 {
		cfg.MaxNoProgress = 3
	}
	return &LoopDetector{
		maxConsecutive: cfg.MaxConsecutive,
		maxErrors:      cfg.MaxErrors,
		maxAlternating: cfg.MaxAlternating,
		maxNoProgress:  cfg.MaxNoProgress,
	}
}

// Record appends a tool invocation to the history.
func (ld *LoopDetector) Record(toolName string, args map[string]any, result string, isError bool) {
	argsHash := hashMap(args)
	resultHash := hashString(result)

	entry := loopEntry{
		ToolName:   toolName,
		ArgsHash:   argsHash,
		ResultHash: resultHash,
		IsError:    isError,
	}
	if isError {
		entry.ErrorMsg = result
	}

	ld.history = append(ld.history, entry)
}

// Check evaluates the recorded history for loop patterns and returns the
// current status along with a human-readable explanation.
// Checks are evaluated in priority order: hard breaks take precedence over warnings.
func (ld *LoopDetector) Check() (LoopStatus, string) {
	n := len(ld.history)
	if n == 0 {
		return LoopStatusOK, ""
	}

	// Strategy 2: repeated error pattern (same tool + same args → same error).
	// Checked before consecutive to give Break priority when errors are involved.
	if status, msg := ld.checkRepeatedErrors(); status != LoopStatusOK {
		return status, msg
	}

	// Strategy 4: no progress (same tool + same args → same result, non-error).
	// Checked before consecutive so that a "same args + same result" pattern
	// triggers a Break rather than just a Warning.
	if status, msg := ld.checkNoProgress(); status != LoopStatusOK {
		return status, msg
	}

	// Strategy 1: identical consecutive tool calls (same tool + same args).
	if status, msg := ld.checkConsecutive(); status != LoopStatusOK {
		return status, msg
	}

	// Strategy 3: alternating A/B/A/B pattern.
	if status, msg := ld.checkAlternating(); status != LoopStatusOK {
		return status, msg
	}

	return LoopStatusOK, ""
}

// Reset clears recorded history.
func (ld *LoopDetector) Reset() {
	ld.history = ld.history[:0]
}

// checkConsecutive detects the same tool+args called back-to-back.
// Warns at maxConsecutive-1, breaks at maxConsecutive.
func (ld *LoopDetector) checkConsecutive() (LoopStatus, string) {
	n := len(ld.history)
	if n < 2 {
		return LoopStatusOK, ""
	}

	last := ld.history[n-1]
	count := 1
	for i := n - 2; i >= 0; i-- {
		e := ld.history[i]
		if e.ToolName == last.ToolName && e.ArgsHash == last.ArgsHash {
			count++
		} else {
			break
		}
	}

	if count >= ld.maxConsecutive {
		return LoopStatusBreak, fmt.Sprintf(
			"loop detected: tool %q called with the same arguments %d times consecutively",
			last.ToolName, count,
		)
	}
	if count >= ld.maxConsecutive-1 {
		return LoopStatusWarning, fmt.Sprintf(
			"potential loop: tool %q called with the same arguments %d times consecutively",
			last.ToolName, count,
		)
	}
	return LoopStatusOK, ""
}

// checkRepeatedErrors detects the same tool call producing the same error.
func (ld *LoopDetector) checkRepeatedErrors() (LoopStatus, string) {
	n := len(ld.history)
	last := ld.history[n-1]
	if !last.IsError {
		return LoopStatusOK, ""
	}

	count := 0
	for _, e := range ld.history {
		if e.ToolName == last.ToolName && e.ArgsHash == last.ArgsHash &&
			e.IsError && e.ErrorMsg == last.ErrorMsg {
			count++
		}
	}

	if count >= ld.maxErrors {
		return LoopStatusBreak, fmt.Sprintf(
			"loop detected: tool %q returned the same error %d times",
			last.ToolName, count,
		)
	}
	return LoopStatusOK, ""
}

// checkAlternating detects an A/B/A/B repetition pattern.
// A "cycle" is one A→B pair; we break after maxAlternating full cycles.
func (ld *LoopDetector) checkAlternating() (LoopStatus, string) {
	n := len(ld.history)
	if n < 4 {
		return LoopStatusOK, ""
	}

	// We need at least 2*maxAlternating entries to confirm the pattern.
	// Check from the tail of history for an A/B repetition.
	type sig struct {
		tool string
		args string
	}

	last := sig{ld.history[n-1].ToolName, ld.history[n-1].ArgsHash}
	prev := sig{ld.history[n-2].ToolName, ld.history[n-2].ArgsHash}

	if last == prev {
		// Not alternating — consecutive instead (handled elsewhere).
		return LoopStatusOK, ""
	}

	// Count how many complete A/B cycles appear at the tail.
	cycles := 0
	for i := n - 1; i >= 1; i -= 2 {
		b := sig{ld.history[i].ToolName, ld.history[i].ArgsHash}
		a := sig{ld.history[i-1].ToolName, ld.history[i-1].ArgsHash}
		if a == prev && b == last {
			cycles++
		} else {
			break
		}
	}

	if cycles >= ld.maxAlternating {
		return LoopStatusBreak, fmt.Sprintf(
			"loop detected: alternating pattern %q/%q repeated %d times",
			prev.tool, last.tool, cycles,
		)
	}
	return LoopStatusOK, ""
}

// checkNoProgress detects a tool returning the same result repeatedly
// (same tool, same args, same result, non-error).
func (ld *LoopDetector) checkNoProgress() (LoopStatus, string) {
	n := len(ld.history)
	last := ld.history[n-1]
	if last.IsError {
		return LoopStatusOK, ""
	}

	count := 0
	for _, e := range ld.history {
		if e.ToolName == last.ToolName && e.ArgsHash == last.ArgsHash &&
			e.ResultHash == last.ResultHash && !e.IsError {
			count++
		}
	}

	if count >= ld.maxNoProgress {
		return LoopStatusBreak, fmt.Sprintf(
			"loop detected: tool %q returned identical results %d times — no progress",
			last.ToolName, count,
		)
	}
	return LoopStatusOK, ""
}

// hashMap produces a stable SHA-256 hex prefix for an arguments map.
func hashMap(m map[string]any) string {
	b, _ := json.Marshal(m)
	return hashString(string(b))
}

// hashString returns the first 16 hex chars of the SHA-256 of s.
func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:8])
}
