package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// defaultModelContextLimits maps provider model name patterns to their token limits.
// Keys are matched as case-insensitive prefix/substring checks.
// This map serves as the built-in defaults; overrides may be applied via SetModelLimit.
var defaultModelContextLimits = map[string]int{
	// Anthropic Claude
	"claude-opus-4":   200_000,
	"claude-sonnet-4": 200_000,
	"claude-haiku-4":  200_000,
	"claude-3-5":      200_000,
	"claude-3-opus":   200_000,
	"claude-3-sonnet": 200_000,
	"claude-3-haiku":  200_000,
	"claude-2":        100_000,
	"claude-instant":  100_000,
	// OpenAI
	"gpt-4o":            128_000,
	"gpt-4-turbo":       128_000,
	"gpt-4-1106":        128_000,
	"gpt-4-0125":        128_000,
	"gpt-4-32k":         32_768,
	"gpt-4":             8_192,
	"gpt-3.5-turbo-16k": 16_385,
	"gpt-3.5-turbo":     4_096,
	"o1-preview":        128_000,
	"o1-mini":           128_000,
	// Ollama / local models
	"gemma4":   131_072,
	"gemma3":   131_072,
	"qwen2.5":  32_768,
	"qwen3":    131_072,
	"phi4":     16_384,
	"llama3.3": 131_072,
	// Generic / unknown
	"default": 128_000,
}

// defaultContextLimit is used when the model name cannot be matched.
const defaultContextLimit = 128_000

// defaultCompactionThreshold is the fraction of the context limit at which compaction triggers.
const defaultCompactionThreshold = 0.80

// ContextManager tracks token usage across a message array and compacts the
// conversation when it approaches the model's context limit.
type ContextManager struct {
	modelName    string
	contextLimit int
	threshold    float64        // fraction of contextLimit that triggers compaction
	compactions  int            // number of times compaction has occurred
	modelLimits  map[string]int // per-instance overrides for model context limits
}

// NewContextManager creates a ContextManager for the given provider.
// The model name is used to look up the context window size.
// compactionThreshold sets the fraction of the context limit that triggers compaction;
// pass 0 to use the default (0.80).
func NewContextManager(providerName string, compactionThreshold float64) *ContextManager {
	if compactionThreshold <= 0 {
		compactionThreshold = defaultCompactionThreshold
	}
	// Copy defaults so per-instance overrides don't pollute the global map.
	limits := make(map[string]int, len(defaultModelContextLimits))
	for k, v := range defaultModelContextLimits {
		limits[k] = v
	}
	limit := lookupContextLimitFrom(limits, providerName)
	return &ContextManager{
		modelName:    providerName,
		contextLimit: limit,
		threshold:    compactionThreshold,
		modelLimits:  limits,
	}
}

// SetModelLimit overrides the context token limit for a specific model name pattern.
// This allows module config to adjust limits without modifying the built-in defaults.
func (cm *ContextManager) SetModelLimit(model string, limit int) {
	if cm.modelLimits == nil {
		cm.modelLimits = make(map[string]int)
	}
	cm.modelLimits[model] = limit
	// Re-derive this manager's limit in case the override matches our model.
	cm.contextLimit = lookupContextLimitFrom(cm.modelLimits, cm.modelName)
}

// SetModelLimitFromProvider applies a context window size reported by the provider,
// overriding the hardcoded lookup. Use when the provider can report its own limit
// (e.g. Ollama context_window config). No-op if limit is zero or negative.
func (cm *ContextManager) SetModelLimitFromProvider(limit int) {
	if limit > 0 {
		cm.contextLimit = limit
	}
}

// lookupContextLimit returns the token limit for a given model/provider name
// using the built-in default limits map.
func lookupContextLimit(name string) int {
	return lookupContextLimitFrom(defaultModelContextLimits, name)
}

// lookupContextLimitFrom returns the token limit for a given model/provider name
// by searching the provided limits map. It matches the longest key that appears
// as a case-insensitive substring, so more specific entries (e.g. "gpt-4-turbo")
// take precedence over shorter ones (e.g. "gpt-4").
func lookupContextLimitFrom(limits map[string]int, name string) int {
	lower := strings.ToLower(name)
	bestLen := 0
	bestLimit := defaultContextLimit
	for key, limit := range limits {
		k := strings.ToLower(key)
		if strings.Contains(lower, k) && len(k) > bestLen {
			bestLen = len(k)
			bestLimit = limit
		}
	}
	return bestLimit
}

// EstimateTokens estimates the number of tokens in a message slice.
// Uses a rough heuristic of 4 characters per token (standard for English text).
func EstimateTokens(messages []provider.Message) int {
	total := 0
	for _, m := range messages {
		// Role adds ~1 token, content at ~4 chars/token, plus ~3 tokens overhead per message
		total += 4 + len(m.Content)/4
	}
	return total
}

// NeedsCompaction returns true if the estimated token count exceeds the threshold.
func (cm *ContextManager) NeedsCompaction(messages []provider.Message) bool {
	estimated := EstimateTokens(messages)
	limit := int(float64(cm.contextLimit) * cm.threshold)
	return estimated >= limit
}

// TokenUsage returns the current estimated tokens and the context limit.
func (cm *ContextManager) TokenUsage(messages []provider.Message) (estimated, limit int) {
	return EstimateTokens(messages), cm.contextLimit
}

// Compact compresses the conversation history by:
//  1. Keeping the system message (index 0) and the most recent 2 exchanges.
//  2. Summarising the middle portion using the LLM provider.
//  3. Injecting the summary as a system-level context note.
//
// If summarisation fails, the middle messages are replaced with a placeholder
// rather than aborting. Returns the compacted message slice.
func (cm *ContextManager) Compact(
	ctx context.Context,
	messages []provider.Message,
	aiProvider provider.Provider,
) []provider.Message {
	// Need at least: system + user + a few more to be worth compacting.
	if len(messages) < 5 {
		return messages
	}

	// Always keep the system prompt (first message).
	system := messages[0]

	// Keep the last 4 messages (2 turns: assistant + tool/user pairs) verbatim
	// so the agent has fresh context to continue from.
	keepTail := 4
	if keepTail >= len(messages)-1 {
		keepTail = len(messages) - 2
	}
	if keepTail < 0 {
		keepTail = 0
	}
	tailStart := len(messages) - keepTail
	tail := messages[tailStart:]
	middle := messages[1:tailStart]

	// Build a summary of the middle portion.
	summary := cm.summarise(ctx, middle, aiProvider)

	cm.compactions++

	// Reconstruct: system → summary note → recent tail.
	summaryNote := provider.Message{
		Role: provider.RoleUser,
		Content: fmt.Sprintf(
			"[CONTEXT COMPACTED - compaction #%d]\n\nSummary of prior conversation:\n%s\n\n"+
				"The conversation continues from this point.",
			cm.compactions, summary,
		),
	}

	compacted := make([]provider.Message, 0, 2+len(tail))
	compacted = append(compacted, system, summaryNote)
	compacted = append(compacted, tail...)
	return compacted
}

// summarise calls the LLM to produce a concise summary of the given messages.
// Falls back to a static placeholder if the LLM call fails.
func (cm *ContextManager) summarise(
	ctx context.Context,
	messages []provider.Message,
	aiProvider provider.Provider,
) string {
	if len(messages) == 0 {
		return "(no prior messages to summarise)"
	}

	// Build a textual transcript for the LLM to summarise.
	var sb strings.Builder
	for _, m := range messages {
		_, _ = fmt.Fprintf(&sb, "[%s]: %s\n\n", m.Role, m.Content)
	}

	summaryReq := []provider.Message{
		{
			Role: provider.RoleSystem,
			Content: "You are a precise summariser. Produce a concise factual summary of the " +
				"following conversation transcript. Preserve key decisions, tool results, and " +
				"facts discovered. Omit greetings and repetition. Use bullet points.",
		},
		{
			Role:    provider.RoleUser,
			Content: "Summarise this conversation:\n\n" + sb.String(),
		},
	}

	resp, err := aiProvider.Chat(ctx, summaryReq, nil)
	if err != nil || resp == nil {
		// Fall back: produce a character-limited excerpt instead.
		text := sb.String()
		if len(text) > 500 {
			text = text[:500] + "... [truncated]"
		}
		return fmt.Sprintf("(auto-summary unavailable; excerpt follows)\n%s", text)
	}

	return resp.Content
}

// Compactions returns how many times compaction has been applied.
func (cm *ContextManager) Compactions() int {
	return cm.compactions
}

// ContextLimitTokens returns the model's context window size in tokens.
func (cm *ContextManager) ContextLimitTokens() int {
	return cm.contextLimit
}
