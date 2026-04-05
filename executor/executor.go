package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/tools"
	"github.com/google/uuid"
)

// Config holds all dependencies for the executor.
// Nil interface fields are replaced with their Null* implementations.
type Config struct {
	// Provider is the AI backend. Required.
	Provider provider.Provider

	// ToolRegistry provides tool definitions and execution. Optional.
	ToolRegistry *tools.Registry

	// Approver handles approval gates. Defaults to NullApprover.
	Approver Approver

	// HumanRequester handles blocking human-request gates. Defaults to NullHumanRequester.
	HumanRequester HumanRequester

	// SecretRedactor redacts secrets from messages. Defaults to NullSecretRedactor.
	SecretRedactor SecretRedactor

	// Transcript records conversation entries. Defaults to NullTranscript.
	Transcript TranscriptRecorder

	// Memory provides persistent agent memory. Defaults to NullMemoryStore.
	Memory MemoryStore

	// MaxIterations caps the agent loop. Default: 10.
	MaxIterations int

	// ApprovalTimeout is how long to wait for human approval. Default: 30m.
	ApprovalTimeout time.Duration

	// RequestTimeout is how long to wait for human request response. Default: 60m.
	RequestTimeout time.Duration

	// LoopDetector config. Zero values use defaults.
	LoopDetection LoopDetectorConfig

	// CompactionThreshold is the fraction of context limit at which compaction triggers. Default: 0.80.
	CompactionThreshold float64

	// TaskID and ProjectID are used for transcript recording.
	TaskID    string
	ProjectID string

	// Inbox receives external messages injected into the conversation
	// between loop iterations. Nil means no external messages.
	Inbox <-chan provider.Message

	// OnEvent is called for each executor event. Nil means no events emitted.
	// The callback must not block — use a buffered channel internally if needed.
	OnEvent func(Event)

	// ShouldStop is called after each tool execution round. If it returns
	// a non-empty string, the loop exits with status "completed" and that
	// string as the Result.Content. Nil means no custom termination.
	ShouldStop func() (reason string)
}

// Result is the outcome of an Execute call.
type Result struct {
	Content    string
	Thinking   string
	Iterations int
	Status     string // "completed", "failed", "loop_detected", "approval_timeout", "request_expired"
	Error      string
}

// Execute runs the autonomous agent loop with the given config.
// systemPrompt is the agent's system instructions; userTask is the task to complete;
// agentID is the agent's identifier used for memory and transcript recording.
func Execute(ctx context.Context, cfg Config, systemPrompt, userTask, agentID string) (*Result, error) {
	// Apply defaults
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 10
	}
	if cfg.ApprovalTimeout <= 0 {
		cfg.ApprovalTimeout = 30 * time.Minute
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 60 * time.Minute
	}
	if cfg.Approver == nil {
		cfg.Approver = &NullApprover{}
	}
	if cfg.HumanRequester == nil {
		cfg.HumanRequester = &NullHumanRequester{}
	}
	if cfg.SecretRedactor == nil {
		cfg.SecretRedactor = &NullSecretRedactor{}
	}
	if cfg.Transcript == nil {
		cfg.Transcript = &NullTranscript{}
	}
	if cfg.Memory == nil {
		cfg.Memory = &NullMemoryStore{}
	}

	if cfg.Provider == nil {
		return nil, fmt.Errorf("executor: Provider is required")
	}

	// Memory injection: augment system prompt with relevant memories.
	if agentID != "" {
		memories, searchErr := cfg.Memory.Search(ctx, agentID, userTask, 5)
		if searchErr == nil && len(memories) > 0 {
			var sb strings.Builder
			sb.WriteString(systemPrompt)
			sb.WriteString("\n\n## Relevant Memory\n")
			for _, m := range memories {
				sb.WriteString("- [")
				sb.WriteString(m.Category)
				sb.WriteString("] ")
				sb.WriteString(m.Content)
				sb.WriteString("\n")
			}
			systemPrompt = sb.String()
		}
	}

	// Build initial conversation
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: systemPrompt},
		{Role: provider.RoleUser, Content: fmt.Sprintf("Task for agent %q:\n\n%s", agentID, userTask)},
	}

	// Get tool definitions
	var toolDefs []provider.ToolDef
	if cfg.ToolRegistry != nil {
		toolDefs = cfg.ToolRegistry.AllDefs()
	}

	// Record initial messages
	for _, msg := range messages {
		_ = cfg.Transcript.Record(ctx, TranscriptEntry{
			ID:        uuid.New().String(),
			AgentID:   agentID,
			TaskID:    cfg.TaskID,
			ProjectID: cfg.ProjectID,
			Iteration: 0,
			Role:      msg.Role,
			Content:   msg.Content,
		})
	}

	var finalContent string
	var finalThinking string
	iterCount := 0
	ld := NewLoopDetector(cfg.LoopDetection)
	cm := NewContextManager(cfg.Provider.Name(), cfg.CompactionThreshold)

	for iterCount < cfg.MaxIterations {
		iterCount++

		// Emit iteration event
		emit(cfg, Event{
			Type:      EventIteration,
			AgentID:   agentID,
			Iteration: iterCount,
		})

		// Drain inbox: append any externally injected messages to the conversation
		// BEFORE the compaction check, so compaction accounts for injected messages.
		var inboxClosed bool
		messages, inboxClosed = drainInbox(ctx, cfg, messages, agentID, iterCount)
		if inboxClosed {
			cfg.Inbox = nil // Avoid unnecessary selects on subsequent iterations.
		}

		// Context window management: compact if approaching model's token limit.
		if cm.NeedsCompaction(messages) {
			messages = cm.Compact(ctx, messages, cfg.Provider)
			_ = cfg.Transcript.Record(ctx, TranscriptEntry{
				ID:        uuid.New().String(),
				AgentID:   agentID,
				TaskID:    cfg.TaskID,
				ProjectID: cfg.ProjectID,
				Iteration: iterCount,
				Role:      provider.RoleUser,
				Content: fmt.Sprintf(
					"[SYSTEM] Context window compacted (compaction #%d).",
					cm.Compactions(),
				),
			})
		}

		// Redact secrets from messages before sending to LLM
		for i := range messages {
			cfg.SecretRedactor.CheckAndRedact(&messages[i])
		}

		resp, err := cfg.Provider.Chat(ctx, messages, toolDefs)
		if err != nil {
			errMsg := fmt.Sprintf("LLM call failed at iteration %d: %v", iterCount, err)
			emit(cfg, Event{
				Type:      EventFailed,
				AgentID:   agentID,
				Iteration: iterCount,
				Error:     errMsg,
			})
			return &Result{
				Content:    errMsg,
				Status:     "failed",
				Iterations: iterCount,
				Error:      errMsg,
			}, nil
		}

		finalContent = resp.Content
		finalThinking = resp.Thinking

		// Emit thinking event
		if resp.Thinking != "" {
			emit(cfg, Event{
				Type:      EventThinking,
				AgentID:   agentID,
				Iteration: iterCount,
				Content:   resp.Thinking,
			})
		}

		// Emit text event
		if resp.Content != "" {
			emit(cfg, Event{
				Type:      EventText,
				AgentID:   agentID,
				Iteration: iterCount,
				Content:   resp.Content,
			})
		}

		// Record assistant response
		_ = cfg.Transcript.Record(ctx, TranscriptEntry{
			ID:        uuid.New().String(),
			AgentID:   agentID,
			TaskID:    cfg.TaskID,
			ProjectID: cfg.ProjectID,
			Iteration: iterCount,
			Role:      provider.RoleAssistant,
			Content:   resp.Content,
			Thinking:  resp.Thinking,
			ToolCalls: resp.ToolCalls,
		})

		// No tool calls — we have a final answer
		if len(resp.ToolCalls) == 0 {
			break
		}

		// Execute tool calls and append results
		messages = append(messages, provider.Message{
			Role:      provider.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		for _, tc := range resp.ToolCalls {
			var resultStr string
			var isError bool

			// Emit tool call start event with a copy of the arguments to prevent
			// any OnEvent handler from mutating the map used by the tool execution.
			emit(cfg, Event{
				Type:       EventToolCallStart,
				AgentID:    agentID,
				Iteration:  iterCount,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				ToolArgs:   copyArgs(tc.Arguments),
			})

			if cfg.ToolRegistry != nil {
				// Build tool context with agent/task IDs
				toolCtx := tools.WithAgentID(ctx, agentID)
				if cfg.TaskID != "" {
					toolCtx = tools.WithTaskID(toolCtx, cfg.TaskID)
				}

				result, execErr := cfg.ToolRegistry.Execute(toolCtx, tc.Name, tc.Arguments)
				if execErr != nil {
					resultStr = fmt.Sprintf("Error: %v", execErr)
					isError = true
				} else {
					resultBytes, _ := json.Marshal(result)
					resultStr = string(resultBytes)
				}
			} else {
				resultStr = "Tool execution not available"
				isError = true
			}

			// Handle approval gates
			if tc.Name == "request_approval" && !isError {
				if approvalOutput, breakLoop := handleApprovalWait(ctx, resultStr, cfg); breakLoop {
					emit(cfg, Event{
						Type:      EventFailed,
						AgentID:   agentID,
						Iteration: iterCount,
						Error:     approvalOutput,
					})
					return &Result{
						Content:    approvalOutput,
						Status:     "approval_timeout",
						Iterations: iterCount,
						Error:      approvalOutput,
					}, nil
				} else {
					resultStr = approvalOutput
				}
			}

			// Handle human request blocking
			if tc.Name == "request_human" && !isError {
				if blockingOutput, breakLoop := handleHumanRequestWait(ctx, resultStr, cfg); breakLoop {
					emit(cfg, Event{
						Type:      EventFailed,
						AgentID:   agentID,
						Iteration: iterCount,
						Error:     blockingOutput,
					})
					return &Result{
						Content:    blockingOutput,
						Status:     "request_expired",
						Iterations: iterCount,
						Error:      blockingOutput,
					}, nil
				} else if blockingOutput != "" {
					resultStr = blockingOutput
				}
			}

			// Redact tool results
			resultStr = cfg.SecretRedactor.Redact(resultStr)

			messages = append(messages, provider.Message{
				Role:       provider.RoleTool,
				Content:    resultStr,
				ToolCallID: tc.ID,
			})

			// Record tool result
			_ = cfg.Transcript.Record(ctx, TranscriptEntry{
				ID:         uuid.New().String(),
				AgentID:    agentID,
				TaskID:     cfg.TaskID,
				ProjectID:  cfg.ProjectID,
				Iteration:  iterCount,
				Role:       provider.RoleTool,
				Content:    resultStr,
				ToolCallID: tc.ID,
			})

			// Emit tool call result event
			emit(cfg, Event{
				Type:       EventToolCallResult,
				AgentID:    agentID,
				Iteration:  iterCount,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				ToolResult: resultStr,
				ToolError:  isError,
			})

			// Loop detection
			ld.Record(tc.Name, tc.Arguments, resultStr, isError)
			loopStatus, loopMsg := ld.Check()
			switch loopStatus {
			case LoopStatusWarning:
				warningContent := fmt.Sprintf("[SYSTEM] Loop warning: %s. Please try a different approach.", loopMsg)
				messages = append(messages, provider.Message{
					Role:    provider.RoleUser,
					Content: warningContent,
				})
				_ = cfg.Transcript.Record(ctx, TranscriptEntry{
					ID:        uuid.New().String(),
					AgentID:   agentID,
					TaskID:    cfg.TaskID,
					ProjectID: cfg.ProjectID,
					Iteration: iterCount,
					Role:      provider.RoleUser,
					Content:   warningContent,
				})
			case LoopStatusBreak:
				breakMsg := fmt.Sprintf("Agent loop terminated: %s", loopMsg)
				_ = cfg.Transcript.Record(ctx, TranscriptEntry{
					ID:        uuid.New().String(),
					AgentID:   agentID,
					TaskID:    cfg.TaskID,
					ProjectID: cfg.ProjectID,
					Iteration: iterCount,
					Role:      provider.RoleUser,
					Content:   "[SYSTEM] " + breakMsg,
				})
				emit(cfg, Event{
					Type:      EventFailed,
					AgentID:   agentID,
					Iteration: iterCount,
					Error:     breakMsg,
				})
				return &Result{
					Content:    breakMsg,
					Status:     "loop_detected",
					Iterations: iterCount,
					Error:      loopMsg,
				}, nil
			}
		}

		// Custom termination: check ShouldStop after all tool calls are processed.
		if cfg.ShouldStop != nil {
			if reason := cfg.ShouldStop(); reason != "" {
				emit(cfg, Event{
					Type:      EventCompleted,
					AgentID:   agentID,
					Iteration: iterCount,
					Content:   reason,
				})
				return &Result{
					Content:    reason,
					Status:     "completed",
					Iterations: iterCount,
					Thinking:   finalThinking,
				}, nil
			}
		}
	}

	// Auto-extraction: save key facts from the conversation to persistent memory.
	if agentID != "" {
		var transcriptBuilder strings.Builder
		for _, msg := range messages {
			if msg.Role == provider.RoleAssistant && msg.Content != "" {
				transcriptBuilder.WriteString(msg.Content)
				transcriptBuilder.WriteString("\n\n")
			}
		}
		if transcriptBuilder.Len() > 0 {
			var embedder provider.Embedder
			if e, ok := provider.AsEmbedder(cfg.Provider); ok {
				embedder = e
			}
			_ = cfg.Memory.ExtractAndSave(ctx, agentID, transcriptBuilder.String(), embedder)
		}
	}

	// Emit completed event
	emit(cfg, Event{
		Type:      EventCompleted,
		AgentID:   agentID,
		Iteration: iterCount,
		Content:   finalContent,
	})

	return &Result{
		Content:    finalContent,
		Thinking:   finalThinking,
		Status:     "completed",
		Iterations: iterCount,
	}, nil
}

// handleApprovalWait parses the request_approval tool result and waits for resolution.
// Returns (message, breakLoop).
func handleApprovalWait(ctx context.Context, toolResult string, cfg Config) (string, bool) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(toolResult), &parsed); err != nil {
		return toolResult, false
	}
	approvalID, _ := parsed["approval_id"].(string)
	if approvalID == "" {
		return toolResult, false
	}

	approval, err := cfg.Approver.WaitForResolution(ctx, approvalID, cfg.ApprovalTimeout)
	if err != nil {
		return fmt.Sprintf("Approval wait error: %v", err), false
	}

	switch approval.Status {
	case ApprovalApproved:
		return fmt.Sprintf("Approval granted. Reviewer comment: %s. You may proceed.", approval.ReviewerComment), false
	case ApprovalRejected:
		return fmt.Sprintf("Approval rejected. Reviewer comment: %s. Please reconsider your approach.", approval.ReviewerComment), false
	case ApprovalTimeout:
		return "Approval request timed out after waiting. Action was not approved within the timeout period.", true
	default:
		return toolResult, false
	}
}

// handleHumanRequestWait parses the request_human tool result and waits for resolution.
// Returns (message, breakLoop).
func handleHumanRequestWait(ctx context.Context, toolResult string, cfg Config) (string, bool) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(toolResult), &parsed); err != nil {
		return "", false
	}
	blocking, _ := parsed["blocking"].(bool)
	if !blocking {
		return "", false
	}
	requestID, _ := parsed["request_id"].(string)
	if requestID == "" {
		return "", false
	}

	req, err := cfg.HumanRequester.WaitForResolution(ctx, requestID, cfg.RequestTimeout)
	if err != nil {
		return fmt.Sprintf("Human request wait error: %v", err), false
	}

	switch req.Status {
	case RequestResolved:
		var msg string
		if req.RequestType == RequestTypeToken {
			secretRef := "the configured secret store"
			var meta map[string]any
			if jsonErr := json.Unmarshal([]byte(req.Metadata), &meta); jsonErr == nil {
				if sn, ok := meta["secret_name"].(string); ok && sn != "" {
					secretRef = fmt.Sprintf("secret %q", sn)
				}
			}
			msg = fmt.Sprintf("Human provided the requested token. It has been stored in %s. Do not request the raw value — read it via the secrets provider.", secretRef)
		} else {
			msg = fmt.Sprintf("Human responded to your request. Response: %s", req.ResponseData)
		}
		if req.ResponseComment != "" {
			msg += fmt.Sprintf(" Comment: %s", req.ResponseComment)
		}
		return msg, false
	case RequestCancelled:
		return fmt.Sprintf("Human cancelled your request. Comment: %s", req.ResponseComment), false
	case RequestExpired:
		return "Human request timed out. No response was received within the timeout period.", true
	default:
		return "", false
	}
}

// emit calls cfg.OnEvent if non-nil. It recovers from any panic in the
// handler so that a misbehaving observer cannot crash the executor run.
func emit(cfg Config, event Event) {
	if cfg.OnEvent == nil {
		return
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("executor: OnEvent handler panicked (event=%s): %v", event.Type, r)
			}
		}()
		cfg.OnEvent(event)
	}()
}

// copyArgs returns a shallow copy of the arguments map so that event handlers
// cannot mutate the original map used by tool execution.
func copyArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	cp := make(map[string]any, len(args))
	for k, v := range args {
		cp[k] = v
	}
	return cp
}

// drainInbox non-blockingly drains all pending messages from cfg.Inbox
// and appends them to the conversation. Returns the updated message slice
// and a boolean indicating whether the inbox channel was closed.
// If cfg.Inbox is nil, returns messages unchanged.
func drainInbox(ctx context.Context, cfg Config, messages []provider.Message, agentID string, iteration int) ([]provider.Message, bool) {
	if cfg.Inbox == nil {
		return messages, false
	}
	for {
		select {
		case msg, ok := <-cfg.Inbox:
			if !ok {
				// Channel closed — stop draining.
				return messages, true
			}
			messages = append(messages, msg)
			_ = cfg.Transcript.Record(ctx, TranscriptEntry{
				ID:        uuid.New().String(),
				AgentID:   agentID,
				TaskID:    cfg.TaskID,
				ProjectID: cfg.ProjectID,
				Iteration: iteration,
				Role:      msg.Role,
				Content:   msg.Content,
			})
		default:
			// No more pending messages.
			return messages, false
		}
	}
}
