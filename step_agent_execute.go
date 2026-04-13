package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/executor"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/tools"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// AgentExecuteStep runs the autonomous agent loop for a single task.
type AgentExecuteStep struct {
	name            string
	maxIterations   int
	providerService string
	app             modular.Application
	tmpl            *module.TemplateEngine
	approvalTimeout time.Duration
	requestTimeout  time.Duration
	loopDetectorCfg executor.LoopDetectorConfig
	compactionThreshold float64
}

func (s *AgentExecuteStep) Name() string { return s.name }

func (s *AgentExecuteStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	if s.app == nil {
		return nil, fmt.Errorf("agent_execute step %q: no application context", s.name)
	}

	// Resolve AI provider
	var aiProvider provider.Provider

	// Peek at pipeline data for provider alias
	peekData := pc.Current
	if row, ok := peekData["row"].(map[string]any); ok {
		for k, v := range row {
			peekData[k] = v
		}
	}
	providerAlias := extractString(peekData, "provider", "")

	// Path 1: Try ProviderRegistry
	for _, regSvcName := range []string{"agent-provider-registry", "ratchet-provider-registry"} {
		if regSvc, ok := s.app.SvcRegistry()[regSvcName]; ok {
			if registry, ok := regSvc.(*ProviderRegistry); ok {
				var regErr error
				if providerAlias != "" && providerAlias != "default" {
					aiProvider, regErr = registry.GetByAlias(ctx, providerAlias)
				} else {
					aiProvider, regErr = registry.GetDefault(ctx)
				}
				if regErr != nil {
					aiProvider = nil
				}
				break
			}
		}
	}

	// Path 2: Fall back to ProviderModule lookup
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
		providerMod, ok := svc.(*ProviderModule)
		if !ok {
			return nil, fmt.Errorf("agent_execute step %q: service %q is not a ProviderModule", s.name, providerSvcName)
		}
		aiProvider = providerMod.Provider()
	}

	// Lazy-lookup tool registry
	var toolRegistry *tools.Registry
	for _, regSvcName := range []string{"agent-tool-registry", "ratchet-tool-registry"} {
		if svc, ok := s.app.SvcRegistry()[regSvcName]; ok {
			toolRegistry, _ = svc.(*tools.Registry)
			break
		}
	}

	// Lazy-lookup optional services
	var approver executor.Approver
	for _, name := range []string{"agent-approval-manager", "ratchet-approval-manager"} {
		if svc, ok := s.app.SvcRegistry()[name]; ok {
			if a, ok := svc.(executor.Approver); ok {
				approver = a
				break
			}
		}
	}

	var humanRequester executor.HumanRequester
	for _, name := range []string{"agent-human-request-manager", "ratchet-human-request-manager"} {
		if svc, ok := s.app.SvcRegistry()[name]; ok {
			if h, ok := svc.(executor.HumanRequester); ok {
				humanRequester = h
				break
			}
		}
	}

	var secretRedactor executor.SecretRedactor
	for _, name := range []string{"agent-secret-guard", "ratchet-secret-guard"} {
		if svc, ok := s.app.SvcRegistry()[name]; ok {
			if r, ok := svc.(executor.SecretRedactor); ok {
				secretRedactor = r
				break
			}
		}
	}

	var transcript executor.TranscriptRecorder
	for _, name := range []string{"agent-transcript-recorder", "ratchet-transcript-recorder"} {
		if svc, ok := s.app.SvcRegistry()[name]; ok {
			if t, ok := svc.(executor.TranscriptRecorder); ok {
				transcript = t
				break
			}
		}
	}

	var memory executor.MemoryStore
	for _, name := range []string{"agent-memory-store", "ratchet-memory-store"} {
		if svc, ok := s.app.SvcRegistry()[name]; ok {
			if m, ok := svc.(executor.MemoryStore); ok {
				memory = m
				break
			}
		}
	}

	// Extract agent and task data from pc.Current
	data := pc.Current
	if row, ok := data["row"].(map[string]any); ok {
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

	// Look up a TrustEvaluator (e.g. GuardrailsModule) from the service registry.
	// Any registered service that satisfies executor.TrustEvaluator is used as the
	// trust engine. This allows agent.guardrails modules to control tool access
	// without a direct import of the orchestrator package.
	var trustEngine executor.TrustEvaluator
	for _, svc := range s.app.SvcRegistry() {
		if te, ok := svc.(executor.TrustEvaluator); ok {
			trustEngine = te
			break
		}
	}

	cfg := executor.Config{
		Provider:            aiProvider,
		ToolRegistry:        toolRegistry,
		Approver:            approver,
		HumanRequester:      humanRequester,
		SecretRedactor:      secretRedactor,
		Transcript:          transcript,
		Memory:              memory,
		MaxIterations:       s.maxIterations,
		ApprovalTimeout:     s.approvalTimeout,
		RequestTimeout:      s.requestTimeout,
		LoopDetection:       s.loopDetectorCfg,
		CompactionThreshold: s.compactionThreshold,
		TaskID:              taskID,
		ProjectID:           projectID,
		TrustEngine:         trustEngine,
	}

	result, err := executor.Execute(ctx, cfg, systemPrompt, taskDescription, agentID)
	if err != nil {
		return nil, fmt.Errorf("agent_execute step %q: %w", s.name, err)
	}

	output := map[string]any{
		"result":     result.Content,
		"status":     result.Status,
		"iterations": result.Iterations,
	}
	if result.Thinking != "" {
		output["thinking"] = result.Thinking
	}
	if result.Error != "" {
		output["error"] = result.Error
	}

	return &module.StepResult{Output: output}, nil
}

// newAgentExecuteStepFactory returns a plugin.StepFactory for "step.agent_execute".
func newAgentExecuteStepFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		maxIterations := extractInt(cfg, "max_iterations", 10)
		if maxIterations <= 0 {
			maxIterations = 10
		}

		providerService, _ := cfg["provider_service"].(string)
		if providerService == "" {
			providerService = "ratchet-ai"
		}

		approvalTimeout := 30 * time.Minute
		if v, ok := cfg["approval_timeout"].(string); ok && v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				approvalTimeout = d
			}
		}

		requestTimeout := 60 * time.Minute
		if v, ok := cfg["request_timeout"].(string); ok && v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				requestTimeout = d
			}
		}

		ldCfg := executor.LoopDetectorConfig{}
		if raw, ok := cfg["loop_detection"].(map[string]any); ok {
			ldCfg.MaxConsecutive = extractInt(raw, "max_consecutive", 0)
			ldCfg.MaxErrors = extractInt(raw, "max_errors", 0)
			ldCfg.MaxAlternating = extractInt(raw, "max_alternating", 0)
			ldCfg.MaxNoProgress = extractInt(raw, "max_no_progress", 0)
		}

		compactionThreshold := 0.0
		if raw, ok := cfg["context"].(map[string]any); ok {
			if v, ok := raw["compaction_threshold"].(float64); ok && v > 0 {
				compactionThreshold = v
			}
		}

		return &AgentExecuteStep{
			name:                name,
			maxIterations:       maxIterations,
			providerService:     providerService,
			app:                 app,
			tmpl:                module.NewTemplateEngine(),
			approvalTimeout:     approvalTimeout,
			requestTimeout:      requestTimeout,
			loopDetectorCfg:     ldCfg,
			compactionThreshold: compactionThreshold,
		}, nil
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

// extractInt reads an integer value from a map by key, accepting both int and float64.
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
