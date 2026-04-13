package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/executor"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	agentplugin "github.com/GoCodeAlone/workflow-plugin-agent"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/google/uuid"
)

// AgentExecuteStep runs the autonomous agent loop for a single task.
type AgentExecuteStep struct {
	name                 string
	maxIterations        int
	providerService      string
	workspace            string // optional workspace path for file tools (overrides env/cwd)
	app                  modular.Application
	tmpl                 *module.TemplateEngine
	approvalTimeout      time.Duration
	requestTimeout       time.Duration
	loopDetectorCfg      LoopDetectorConfig
	subAgentMaxPerParent int
	subAgentMaxDepth     int
	compactionThreshold  float64
	browserMaxTextLen    int
	inputFromBlackboard  InputFromBlackboard
	hasBlackboardInput   bool
	parallelToolCalls    bool
}

func (s *AgentExecuteStep) Name() string { return s.name }

func (s *AgentExecuteStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	if s.app == nil {
		return nil, fmt.Errorf("agent_execute step %q: no application context", s.name)
	}

	// Blackboard input injection (default / pc.Current mode only).
	// system_prompt_append and user_message modes are handled after systemPrompt is built.
	var blackboardPromptContent string
	if s.hasBlackboardInput {
		content, err := InjectBlackboardInput(ctx, s.app, s.inputFromBlackboard, pc)
		if err != nil {
			// Non-fatal: log and continue without blackboard input
			if logger := s.app.Logger(); logger != nil {
				logger.Warn("agent_execute: blackboard input injection failed", "error", err, "step", s.name)
			}
		} else {
			blackboardPromptContent = content
		}
	}

	// Resolve AI provider via multiple paths:
	// 1. Try ProviderRegistry (DB-backed providers) if available
	// 2. Fall back to AIProviderModule (YAML-configured) lookup
	var aiProvider provider.Provider

	// Extract provider alias from pipeline data (set by agent's provider column)
	// We do this after flattening below, but we peek at data here for the alias.
	peekData := pc.Current
	if row, ok := peekData["row"].(map[string]any); ok {
		for k, v := range row {
			peekData[k] = v
		}
	}
	providerAlias := extractString(peekData, "provider", "")

	// Path 1: Try ProviderRegistry
	if regSvc, ok := s.app.SvcRegistry()["ratchet-provider-registry"]; ok {
		if registry, ok := regSvc.(*ProviderRegistry); ok {
			var regErr error
			if providerAlias != "" && providerAlias != "default" {
				aiProvider, regErr = registry.GetByAlias(ctx, providerAlias)
			} else {
				aiProvider, regErr = registry.GetDefault(ctx)
			}
			if regErr != nil {
				aiProvider = nil // fall through to path 2
			}
		}
	}

	// Path 2: Fall back to provider module lookup (handles both ratchet and agent plugin modules)
	if aiProvider == nil {
		providerSvcRaw, err := s.tmpl.Resolve(s.providerService, pc)
		if err != nil {
			return nil, fmt.Errorf("agent_execute step %q: resolve provider_service: %w", s.name, err)
		}
		providerSvcName := fmt.Sprintf("%v", providerSvcRaw)

		svc, ok := s.app.SvcRegistry()[providerSvcName]
		if !ok {
			return nil, fmt.Errorf("agent_execute step %q: provider service %q not found", s.name, providerSvcName)
		}
		// Handle both the legacy AIProviderModule (ratchet.ai_provider) and the new
		// ProviderModule (agent.provider) from workflow-plugin-agent.
		switch mod := svc.(type) {
		case *AIProviderModule:
			aiProvider = mod.Provider()
		case *agentplugin.ProviderModule:
			aiProvider = mod.Provider()
		default:
			return nil, fmt.Errorf("agent_execute step %q: service %q is not a recognized provider module (got %T)", s.name, providerSvcName, svc)
		}
	}

	// Lazy-lookup services from the registry. These are registered by wiring hooks
	// which run AFTER step factories, so they may not be available at factory time.
	var toolRegistry *ToolRegistry
	if svc, ok := s.app.SvcRegistry()["ratchet-tool-registry"]; ok {
		toolRegistry, _ = svc.(*ToolRegistry)
	}

	// Apply browser text length override from step config if set.
	if s.browserMaxTextLen > 0 && toolRegistry != nil {
		if tool, ok := toolRegistry.Get("browser_navigate"); ok {
			if bt, ok := tool.(*tools.BrowserNavigateTool); ok {
				bt.MaxTextLength = s.browserMaxTextLen
			}
		}
	}
	var guard *SecretGuard
	if svc, ok := s.app.SvcRegistry()["ratchet-secret-guard"]; ok {
		guard, _ = svc.(*SecretGuard)
	}
	var recorder *TranscriptRecorder
	if svc, ok := s.app.SvcRegistry()["ratchet-transcript-recorder"]; ok {
		recorder, _ = svc.(*TranscriptRecorder)
	}
	var containerMgr *ContainerManager
	if svc, ok := s.app.SvcRegistry()["ratchet-container-manager"]; ok {
		containerMgr, _ = svc.(*ContainerManager)
	}
	// Look up guardrails module (optional). If present, tool calls are checked before execution.
	guardrails := findGuardrailsModule(s.app)

	// Extract agent and task data from pc.Current.
	// The find-pending-task db_query step returns data under a "row" key,
	// so we also check pc.Current["row"] for nested data.
	data := pc.Current
	if row, ok := data["row"].(map[string]any); ok {
		// Merge row fields into a flat lookup map (row fields take precedence)
		flat := make(map[string]any, len(data)+len(row))
		for k, v := range data {
			flat[k] = v
		}
		for k, v := range row {
			flat[k] = v
		}
		data = flat
	}
	systemPrompt := extractString(data, "system_prompt", "You are a helpful AI agent.")
	taskDescription := extractString(data, "description", extractString(data, "task", "Complete the assigned task."))
	agentName := extractString(data, "agent_name", extractString(data, "name", "agent"))
	agentID := extractString(data, "agent_id", agentName)
	taskID := extractString(data, "task_id", extractString(data, "id", ""))
	projectID := extractString(data, "project_id", "")
	teamID := extractString(data, "team_id", "")

	// Log provider resolution for debugging
	if s.app != nil {
		if logger := s.app.Logger(); logger != nil {
			logger.Info("agent_execute: provider resolved",
				"agent", agentName,
				"provider_alias", providerAlias,
				"provider_name", aiProvider.Name(),
			)
		}
	}

	// Build enriched context with workspace/container info
	toolCtx := ctx
	// Inject agent, task, and team IDs so tools and policies can retrieve them.
	toolCtx = tools.WithAgentID(toolCtx, agentID)
	toolCtx = tools.WithTaskID(toolCtx, taskID)
	if teamID != "" {
		toolCtx = WithTeamID(toolCtx, teamID)
	}
	if projectID != "" {
		toolCtx = tools.WithProjectID(toolCtx, projectID)

		// Look up project workspace path from DB
		if s.app != nil {
			if svc, ok := s.app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok && dbp.DB() != nil {
					var wsPath string
					row := dbp.DB().QueryRowContext(ctx,
						"SELECT workspace_path FROM projects WHERE id = ?", projectID,
					)
					if row.Scan(&wsPath) == nil && wsPath != "" {
						toolCtx = tools.WithWorkspacePath(toolCtx, wsPath)
					}
				}
			}
		}

		// If container manager is available, inject it as ContainerExecer
		if containerMgr != nil && containerMgr.IsAvailable() {
			toolCtx = context.WithValue(toolCtx, tools.ContextKeyContainerID, tools.ContainerExecer(containerMgr))
		}
	}

	// Inject step-config workspace as fallback when no project workspace was resolved.
	if s.workspace != "" {
		if ws, ok := tools.WorkspacePathFromContext(toolCtx); !ok || ws == "" {
			toolCtx = tools.WithWorkspacePath(toolCtx, s.workspace)
		}
	}

	// Skill injection: augment system prompt with assigned skill content.
	if svc, ok := s.app.SvcRegistry()["ratchet-skill-manager"]; ok {
		if sm, ok := svc.(*SkillManager); ok {
			if skillPrompt, err := sm.BuildSkillPrompt(ctx, agentID); err == nil && skillPrompt != "" {
				systemPrompt = systemPrompt + "\n\n" + skillPrompt
			}
		}
	}

	// Memory injection: augment system prompt with relevant memories before building messages.
	var memoryStore *MemoryStore
	if svc, ok := s.app.SvcRegistry()["ratchet-memory-store"]; ok {
		memoryStore, _ = svc.(*MemoryStore)
	}
	if memoryStore != nil && agentID != "" {
		memories, searchErr := memoryStore.Search(ctx, agentID, taskDescription, 5)
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

	// Apply blackboard content injection now that systemPrompt is fully built.
	if blackboardPromptContent != "" {
		switch s.inputFromBlackboard.InjectAs {
		case "system_prompt_append":
			systemPrompt = systemPrompt + "\n\n## Blackboard Context\n" + blackboardPromptContent
		}
	}

	// Build initial conversation
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: systemPrompt},
		{Role: provider.RoleUser, Content: fmt.Sprintf("Task for agent %q:\n\n%s", agentName, taskDescription)},
	}

	// Inject blackboard content as a user message (before the agent loop begins).
	if blackboardPromptContent != "" && s.inputFromBlackboard.InjectAs == "user_message" {
		messages = append(messages, provider.Message{
			Role:    provider.RoleUser,
			Content: "## Context from Blackboard\n" + blackboardPromptContent,
		})
	}

	// Get tool definitions
	var toolDefs []provider.ToolDef
	if toolRegistry != nil {
		toolDefs = toolRegistry.AllDefs()
	}

	// Record system prompt and user message
	if recorder != nil {
		for _, msg := range messages {
			_ = recorder.Record(ctx, TranscriptEntry{
				ID:        uuid.New().String(),
				AgentID:   agentID,
				TaskID:    taskID,
				ProjectID: projectID,
				Iteration: 0,
				Role:      msg.Role,
				Content:   msg.Content,
			})
		}
	}

	// Detect if provider manages server-side context (sends only delta messages).
	var contextStrategy provider.ContextStrategy
	if cs, ok := aiProvider.(provider.ContextStrategy); ok && cs.ManagesContext() {
		contextStrategy = cs
	}

	var finalContent string
	iterCount := 0
	lastSentIndex := 0
	emptyRetries := 0
	intentRetries := 0
	ld := NewLoopDetector(s.loopDetectorCfg)
	cm := NewContextManager(aiProvider.Name(), s.compactionThreshold)
	// If the provider reports its own context window, use that for compaction.
	if cw, ok := aiProvider.(interface{ ContextWindow() int }); ok {
		cm.SetModelLimitFromProvider(cw.ContextWindow())
	}

	// Wire response paginator so large tool outputs are paginated instead of truncated.
	if toolRegistry != nil {
		contextWindow := defaultContextLimit
		if cw, ok := aiProvider.(interface{ ContextWindow() int }); ok {
			if w := cw.ContextWindow(); w > 0 {
				contextWindow = w
			}
		}
		toolRegistry.SetPaginator(NewResponsePaginator(contextWindow))
	}

	for iterCount < s.maxIterations {
		iterCount++

		// Context window management: compact if approaching model's token limit.
		if cm.NeedsCompaction(messages) {
			estimated, limit := cm.TokenUsage(messages)
			if s.app != nil {
				if logger := s.app.Logger(); logger != nil {
					logger.Info("agent_execute: compacting context",
						"agent", agentName,
						"iteration", iterCount,
						"estimated_tokens", estimated,
						"limit", limit,
						"compaction_num", cm.Compactions()+1,
					)
				}
			}
			if contextStrategy != nil {
				if resetErr := contextStrategy.ResetContext(ctx); resetErr != nil {
					if s.app != nil {
						if logger := s.app.Logger(); logger != nil {
							logger.Warn("agent_execute: context reset failed",
								"agent", agentName, "error", resetErr)
						}
					}
				}
			}
			messages = cm.Compact(ctx, messages, aiProvider)
			if contextStrategy != nil {
				lastSentIndex = 0 // resend full compacted history after reset
			}
			if recorder != nil {
				_ = recorder.Record(ctx, TranscriptEntry{
					ID:        uuid.New().String(),
					AgentID:   agentID,
					TaskID:    taskID,
					ProjectID: projectID,
					Iteration: iterCount,
					Role:      provider.RoleUser,
					Content: fmt.Sprintf(
						"[SYSTEM] Context window compacted (compaction #%d). Estimated %d tokens of %d limit.",
						cm.Compactions(), estimated, limit,
					),
				})
			}
		}

		// Redact secrets from messages before sending to LLM
		if guard != nil {
			for i := range messages {
				guard.CheckAndRedact(&messages[i])
			}
		}

		// Filter tool definitions to only those permitted by guardrails.
		filteredToolDefs := toolDefs
		if guardrails != nil {
			filteredToolDefs = guardrails.FilterTools(toolDefs)
		}

		// For stateful providers, send only new messages since the last call.
		chatMessages := messages
		if contextStrategy != nil {
			chatMessages = messages[lastSentIndex:]
		}
		resp, err := aiProvider.Chat(ctx, chatMessages, filteredToolDefs)
		if contextStrategy != nil {
			lastSentIndex = len(messages)
		}
		if err != nil {
			// Don't abort the pipeline — return a failed result so the task can be marked.
			errMsg := fmt.Sprintf("LLM call failed at iteration %d: %v", iterCount, err)
			if s.app != nil {
				if logger := s.app.Logger(); logger != nil {
					logger.Error("agent_execute: chat failed", "agent", agentName, "iteration", iterCount, "error", err)
				}
			}
			if sseHub := findSSEHub(s.app); sseHub != nil {
				eventData, _ := json.Marshal(map[string]any{
					"task_id":  taskID,
					"agent_id": agentID,
					"status":   "failed",
				})
				sseHub.BroadcastEvent("task_completed", string(eventData))
			}
			output := map[string]any{
				"result":     errMsg,
				"status":     "failed",
				"iterations": iterCount,
				"error":      errMsg,
			}
			return &module.StepResult{Output: output}, nil
		}

		finalContent = resp.Content

		// Record assistant response
		if recorder != nil {
			_ = recorder.Record(ctx, TranscriptEntry{
				ID:        uuid.New().String(),
				AgentID:   agentID,
				TaskID:    taskID,
				ProjectID: projectID,
				Iteration: iterCount,
				Role:      provider.RoleAssistant,
				Content:   resp.Content,
				ToolCalls: resp.ToolCalls,
			})
		}

		// No tool calls — check for empty response or verbalized tool intent.
		if len(resp.ToolCalls) == 0 {
			content := strings.TrimSpace(resp.Content)

			if content == "" && emptyRetries < 2 {
				messages = append(messages, provider.Message{
					Role:    provider.RoleAssistant,
					Content: "",
				})
				messages = append(messages, provider.Message{
					Role: provider.RoleUser,
					Content: "[SYSTEM] Your response was empty. Please continue with the next step. " +
						"If you are done, respond with TASK COMPLETE and a summary.",
				})
				emptyRetries++
				iterCount-- // don't count toward max
				continue
			}

			if containsToolIntent(content, toolDefs) && intentRetries < 2 {
				messages = append(messages, provider.Message{
					Role:    provider.RoleAssistant,
					Content: content,
				})
				messages = append(messages, provider.Message{
					Role: provider.RoleUser,
					Content: "[SYSTEM] You described your intent to call a tool but didn't actually " +
						"make the tool call. Please execute the tool by making a proper tool call.",
				})
				intentRetries++
				iterCount--
				continue
			}

			// Real completion — break.
			break
		}

		// In sequential mode, reject multiple tool calls and ask the LLM to resend only one.
		if !s.parallelToolCalls && len(resp.ToolCalls) > 1 {
			errMsg := fmt.Sprintf(
				"[SYSTEM] You sent %d tool calls in one turn, but this agent requires sequential execution. "+
					"Call ONE tool at a time and wait for its result. "+
					"Your first intended call was %q — resend it as the only tool call.",
				len(resp.ToolCalls), resp.ToolCalls[0].Name,
			)
			messages = append(messages, provider.Message{
				Role:      provider.RoleAssistant,
				Content:   resp.Content,
				ToolCalls: resp.ToolCalls,
			})
			messages = append(messages, provider.Message{
				Role:    provider.RoleUser,
				Content: errMsg,
			})
			iterCount-- // correction turn; don't count toward max_iterations
			continue
		}

		toolCallsToProcess := resp.ToolCalls

		// Execute tool calls and append results
		messages = append(messages, provider.Message{
			Role:      provider.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: toolCallsToProcess,
		})

		for _, tc := range toolCallsToProcess {
			var resultStr string
			var isError bool

			// Guardrails check: validate tool access and command safety before execution.
			if guardrails != nil {
				action := guardrails.Evaluate(toolCtx, tc.Name, tc.Arguments)
				if action == executor.ActionDeny {
					resultStr = fmt.Sprintf("guardrails: tool %q is not permitted", tc.Name)
					isError = true
				} else if cmdStr, _ := tc.Arguments["command"].(string); cmdStr != "" {
					// For shell/bash tools, also check command safety.
					cmdAction := guardrails.EvaluateCommand(cmdStr)
					if cmdAction == executor.ActionDeny {
						resultStr = fmt.Sprintf("guardrails: command blocked by safety policy")
						isError = true
					}
				}
			}

			if !isError {
				if toolRegistry != nil {
					result, execErr := toolRegistry.Execute(toolCtx, tc.Name, tc.Arguments)
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
			}

			// Handle approval gates: if the tool was request_approval, pause and wait.
			if tc.Name == "request_approval" && !isError {
				if approvalOutput, breakLoop := s.handleApprovalWait(ctx, resultStr, agentName, iterCount); breakLoop {
					if sseHub := findSSEHub(s.app); sseHub != nil {
						eventData, _ := json.Marshal(map[string]any{
							"task_id":  taskID,
							"agent_id": agentID,
							"status":   "approval_timeout",
						})
						sseHub.BroadcastEvent("task_completed", string(eventData))
					}
					output := map[string]any{
						"result":     approvalOutput,
						"status":     "approval_timeout",
						"iterations": iterCount,
						"error":      approvalOutput,
					}
					return &module.StepResult{Output: output}, nil
				} else {
					// Continue: replace resultStr with the resolution message
					resultStr = approvalOutput
				}
			}

			// Handle human request blocking: if the tool was request_human with blocking=true, pause and wait.
			if tc.Name == "request_human" && !isError {
				if blockingOutput, breakLoop := s.handleHumanRequestWait(ctx, resultStr, agentName, iterCount); breakLoop {
					if sseHub := findSSEHub(s.app); sseHub != nil {
						eventData, _ := json.Marshal(map[string]any{
							"task_id":  taskID,
							"agent_id": agentID,
							"status":   "request_expired",
						})
						sseHub.BroadcastEvent("task_completed", string(eventData))
					}
					output := map[string]any{
						"result":     blockingOutput,
						"status":     "request_expired",
						"iterations": iterCount,
						"error":      blockingOutput,
					}
					return &module.StepResult{Output: output}, nil
				} else if blockingOutput != "" {
					resultStr = blockingOutput
				}
			}

			// Redact tool results
			if guard != nil {
				resultStr = guard.Redact(resultStr)
			}

			messages = append(messages, provider.Message{
				Role:       provider.RoleTool,
				Content:    resultStr,
				ToolCallID: tc.ID,
			})

			// Record tool result
			if recorder != nil {
				_ = recorder.Record(ctx, TranscriptEntry{
					ID:         uuid.New().String(),
					AgentID:    agentID,
					TaskID:     taskID,
					ProjectID:  projectID,
					Iteration:  iterCount,
					Role:       provider.RoleTool,
					Content:    resultStr,
					ToolCallID: tc.ID,
				})
			}

			// Loop detection: record and check after each tool execution.
			ld.Record(tc.Name, tc.Arguments, resultStr, isError)
			loopStatus, loopMsg := ld.Check()
			switch loopStatus {
			case LoopStatusWarning:
				warningContent := fmt.Sprintf("[SYSTEM] Loop warning: %s. Please try a different approach.", loopMsg)
				messages = append(messages, provider.Message{
					Role:    provider.RoleUser,
					Content: warningContent,
				})
				if recorder != nil {
					_ = recorder.Record(ctx, TranscriptEntry{
						ID:        uuid.New().String(),
						AgentID:   agentID,
						TaskID:    taskID,
						ProjectID: projectID,
						Iteration: iterCount,
						Role:      provider.RoleUser,
						Content:   warningContent,
					})
				}
			case LoopStatusBreak:
				breakMsg := fmt.Sprintf("Agent loop terminated: %s", loopMsg)
				if s.app != nil {
					if logger := s.app.Logger(); logger != nil {
						logger.Warn("agent_execute: loop detected, breaking",
							"agent", agentName, "iteration", iterCount, "reason", loopMsg)
					}
				}
				if recorder != nil {
					_ = recorder.Record(ctx, TranscriptEntry{
						ID:        uuid.New().String(),
						AgentID:   agentID,
						TaskID:    taskID,
						ProjectID: projectID,
						Iteration: iterCount,
						Role:      provider.RoleUser,
						Content:   "[SYSTEM] " + breakMsg,
					})
				}
				if sseHub := findSSEHub(s.app); sseHub != nil {
					eventData, _ := json.Marshal(map[string]any{
						"task_id":  taskID,
						"agent_id": agentID,
						"status":   "loop_detected",
					})
					sseHub.BroadcastEvent("task_completed", string(eventData))
				}
				output := map[string]any{
					"result":     breakMsg,
					"status":     "loop_detected",
					"iterations": iterCount,
					"error":      loopMsg,
				}
				return &module.StepResult{Output: output}, nil
			}
		}
	}

	// Cancel any orphaned sub-agent tasks when the parent agent completes.
	if svc, ok := s.app.SvcRegistry()["ratchet-sub-agent-manager"]; ok {
		if sam, ok := svc.(*SubAgentManager); ok {
			if cancelErr := sam.CancelChildren(ctx, agentID); cancelErr != nil {
				if s.app != nil {
					if logger := s.app.Logger(); logger != nil {
						logger.Warn("agent_execute: failed to cancel sub-agent children",
							"agent", agentName, "error", cancelErr)
					}
				}
			}
		}
	}

	// Auto-extraction: save key facts from the conversation to persistent memory.
	if memoryStore != nil && agentID != "" {
		var transcriptBuilder strings.Builder
		for _, msg := range messages {
			if msg.Role == provider.RoleAssistant && msg.Content != "" {
				transcriptBuilder.WriteString(msg.Content)
				transcriptBuilder.WriteString("\n\n")
			}
		}
		if transcriptBuilder.Len() > 0 {
			var embedder provider.Embedder
			if e, ok := provider.AsEmbedder(aiProvider); ok {
				embedder = e
			}
			if extractErr := memoryStore.ExtractAndSave(ctx, agentID, transcriptBuilder.String(), embedder); extractErr != nil {
				if s.app != nil {
					if logger := s.app.Logger(); logger != nil {
						logger.Warn("agent_execute: failed to extract and save memory",
							"agent", agentName, "error", extractErr)
					}
				}
			}
		}
	}

	// Broadcast task completion via SSE
	if sseHub := findSSEHub(s.app); sseHub != nil {
		eventData, _ := json.Marshal(map[string]any{
			"task_id":  taskID,
			"agent_id": agentID,
			"status":   "completed",
		})
		sseHub.BroadcastEvent("task_completed", string(eventData))
	}

	output := map[string]any{
		"result":     finalContent,
		"status":     "completed",
		"iterations": iterCount,
	}

	return &module.StepResult{Output: output}, nil
}

// findSSEHub searches the service registry for an SSEHub instance.
func findSSEHub(app modular.Application) *SSEHub {
	for _, svc := range app.SvcRegistry() {
		if hub, ok := svc.(*SSEHub); ok {
			return hub
		}
	}
	return nil
}

// findGuardrailsModule searches the service registry for a GuardrailsModule instance.
// Returns nil if no guardrails module is registered.
func findGuardrailsModule(app modular.Application) *GuardrailsModule {
	for _, svc := range app.SvcRegistry() {
		if gm, ok := svc.(*GuardrailsModule); ok {
			return gm
		}
	}
	return nil
}

// handleApprovalWait parses the request_approval tool result, finds the ApprovalManager,
// and waits for resolution. Returns (message, breakLoop):
//   - breakLoop=true means the approval timed out and the loop should stop.
//   - breakLoop=false means continue with the provided message.
func (s *AgentExecuteStep) handleApprovalWait(ctx context.Context, toolResult, agentName string, iterCount int) (string, bool) {
	// Parse the approval ID from the tool result JSON
	var parsed map[string]any
	if err := json.Unmarshal([]byte(toolResult), &parsed); err != nil {
		return toolResult, false // not parseable, just continue
	}
	approvalID, _ := parsed["approval_id"].(string)
	if approvalID == "" {
		return toolResult, false // no approval ID, just continue
	}

	// Lazy-lookup ApprovalManager
	var am *ApprovalManager
	if svc, ok := s.app.SvcRegistry()["ratchet-approval-manager"]; ok {
		am, _ = svc.(*ApprovalManager)
	}
	if am == nil {
		// No manager available — just continue without blocking
		return toolResult, false
	}

	if s.app != nil {
		if logger := s.app.Logger(); logger != nil {
			logger.Info("agent_execute: waiting for approval",
				"agent", agentName, "iteration", iterCount, "approval_id", approvalID)
		}
	}

	// Wait for resolution up to the configured approval timeout (default 30m).
	timeout := s.approvalTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	approval, err := am.WaitForResolution(ctx, approvalID, timeout)
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

// handleHumanRequestWait parses the request_human tool result, checks if blocking=true,
// finds the HumanRequestManager, and waits for resolution. Returns (message, breakLoop):
//   - breakLoop=true means the request expired and the loop should stop.
//   - breakLoop=false and non-empty message means continue with that message.
//   - breakLoop=false and empty message means non-blocking, just continue.
func (s *AgentExecuteStep) handleHumanRequestWait(ctx context.Context, toolResult, agentName string, iterCount int) (string, bool) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(toolResult), &parsed); err != nil {
		return "", false
	}
	blocking, _ := parsed["blocking"].(bool)
	if !blocking {
		return "", false // non-blocking, just continue
	}
	requestID, _ := parsed["request_id"].(string)
	if requestID == "" {
		return "", false
	}

	var hrm *HumanRequestManager
	if svc, ok := s.app.SvcRegistry()["ratchet-human-request-manager"]; ok {
		hrm, _ = svc.(*HumanRequestManager)
	}
	if hrm == nil {
		return "", false
	}

	if s.app != nil {
		if logger := s.app.Logger(); logger != nil {
			logger.Info("agent_execute: waiting for human request resolution",
				"agent", agentName, "iteration", iterCount, "request_id", requestID)
		}
	}

	timeout := s.requestTimeout
	if timeout <= 0 {
		timeout = 60 * time.Minute
	}
	req, err := hrm.WaitForResolution(ctx, requestID, timeout)
	if err != nil {
		return fmt.Sprintf("Human request wait error: %v", err), false
	}

	switch req.Status {
	case RequestResolved:
		var msg string
		if req.RequestType == RequestTypeToken {
			// Do not leak secret values into the agent transcript/LLM context.
			// Reference the secret_name from metadata so the agent can read via SecretGuard.
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

// extractString safely pulls a string value from a map.
func extractString(m map[string]any, key, defaultVal string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return defaultVal
}

// newAgentExecuteStepFactory returns a plugin.StepFactory for "step.agent_execute".
//
// Supported config keys:
//
//	max_iterations        int    — max agent loop iterations (default 10)
//	provider_service      string — service registry key for the AI provider (default "ratchet-ai")
//	workspace             string — workspace directory injected into file-tool context (fallback when no DB project)
//	approval_timeout      string — duration string for approval wait, e.g. "60m" (default "30m")
//	loop_detection:
//	  max_consecutive     int    — default 3
//	  max_errors          int    — default 2
//	  max_alternating     int    — default 3
//	  max_no_progress     int    — default 3
//	sub_agent:
//	  max_per_parent      int    — default 5
//	  max_depth           int    — default 1
//	context:
//	  compaction_threshold float64 — default 0.80
//	browser:
//	  max_text_length     int    — default 2000
func newAgentExecuteStepFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		maxIterations := 10
		switch v := cfg["max_iterations"].(type) {
		case int:
			maxIterations = v
		case float64:
			maxIterations = int(v)
		}
		if maxIterations <= 0 {
			maxIterations = 10
		}

		providerService, _ := cfg["provider_service"].(string)
		if providerService == "" {
			providerService = "ratchet-ai"
		}

		workspace, _ := cfg["workspace"].(string)

		// approval_timeout: duration string, e.g. "30m", "1h". Default 30m.
		approvalTimeout := 30 * time.Minute
		if v, ok := cfg["approval_timeout"].(string); ok && v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				approvalTimeout = d
			}
		}

		// request_timeout: duration string, e.g. "60m", "2h". Default 60m.
		requestTimeout := 60 * time.Minute
		if v, ok := cfg["request_timeout"].(string); ok && v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				requestTimeout = d
			}
		}

		// loop_detection sub-map
		ldCfg := LoopDetectorConfig{}
		if raw, ok := cfg["loop_detection"].(map[string]any); ok {
			ldCfg.MaxConsecutive = extractInt(raw, "max_consecutive", 0)
			ldCfg.MaxErrors = extractInt(raw, "max_errors", 0)
			ldCfg.MaxAlternating = extractInt(raw, "max_alternating", 0)
			ldCfg.MaxNoProgress = extractInt(raw, "max_no_progress", 0)
		}

		// sub_agent sub-map
		subAgentMaxPerParent := 0
		subAgentMaxDepth := 0
		if raw, ok := cfg["sub_agent"].(map[string]any); ok {
			subAgentMaxPerParent = extractInt(raw, "max_per_parent", 0)
			subAgentMaxDepth = extractInt(raw, "max_depth", 0)
		}

		// context sub-map
		compactionThreshold := 0.80
		if raw, ok := cfg["context"].(map[string]any); ok {
			if v, ok := raw["compaction_threshold"].(float64); ok && v > 0 {
				compactionThreshold = v
			}
		}

		// browser sub-map
		browserMaxTextLen := 0
		if raw, ok := cfg["browser"].(map[string]any); ok {
			browserMaxTextLen = extractInt(raw, "max_text_length", 0)
		}

		// input_from_blackboard: optional config for reading blackboard artifacts into agent context.
		ibb, hasIBB := parseInputFromBlackboard(cfg)

		// parallel_tool_calls: default true. Set false to execute one tool call per LLM turn.
		parallelToolCalls := true
		if v, ok := cfg["parallel_tool_calls"].(bool); ok {
			parallelToolCalls = v
		}

		return &AgentExecuteStep{
			name:                 name,
			maxIterations:        maxIterations,
			providerService:      providerService,
			workspace:            workspace,
			app:                  app,
			tmpl:                 module.NewTemplateEngine(),
			approvalTimeout:      approvalTimeout,
			requestTimeout:       requestTimeout,
			loopDetectorCfg:      ldCfg,
			subAgentMaxPerParent: subAgentMaxPerParent,
			subAgentMaxDepth:     subAgentMaxDepth,
			compactionThreshold:  compactionThreshold,
			browserMaxTextLen:    browserMaxTextLen,
			inputFromBlackboard:  ibb,
			hasBlackboardInput:   hasIBB,
			parallelToolCalls:    parallelToolCalls,
		}, nil
	}
}

// extractInt reads an integer value from a map by key, accepting both int and float64.
// Returns defaultVal if the key is absent or zero.
func extractInt(m map[string]any, key string, defaultVal int) int {
	v, ok := m[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return defaultVal
}
