// Package ratchetplugin is a workflow EnginePlugin that provides
// ratchet-specific module types, pipeline steps, and wiring hooks.
package orchestrator

import (
	"context"
	"database/sql"
	"os"
	"strings"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	agentplugin "github.com/GoCodeAlone/workflow-plugin-agent"
	"github.com/GoCodeAlone/workflow-plugin-authz/authz"
	"github.com/GoCodeAlone/workflow/capability"
	"github.com/GoCodeAlone/workflow/config"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/GoCodeAlone/workflow/schema"
	"github.com/GoCodeAlone/workflow/secrets"
)

// RatchetPlugin implements plugin.EnginePlugin.
type RatchetPlugin struct {
	plugin.BaseEnginePlugin
}

// New creates a new RatchetPlugin ready to register with the workflow engine.
func New() *RatchetPlugin {
	return &RatchetPlugin{
		BaseEnginePlugin: plugin.BaseEnginePlugin{
			BaseNativePlugin: plugin.BaseNativePlugin{
				PluginName:        "ratchet",
				PluginVersion:     "1.0.0",
				PluginDescription: "Ratchet autonomous agent orchestration",
			},
			Manifest: plugin.PluginManifest{
				Name:        "ratchet",
				Version:     "1.0.0",
				Author:      "GoCodeAlone",
				Description: "Ratchet autonomous agent orchestration plugin",
				ModuleTypes: []string{"agent.provider", "ratchet.sse_hub", "ratchet.scheduler", "ratchet.mcp_client", "ratchet.mcp_server", "authz.casbin"},
				StepTypes:   []string{"step.agent_execute", "step.provider_test", "step.provider_models", "step.model_pull", "step.workspace_init", "step.container_control", "step.secret_manage", "step.vault_config", "step.mcp_reload", "step.oauth_exchange", "step.approval_resolve", "step.webhook_process", "step.security_audit", "step.test_interact", "step.human_request_resolve", "step.memory_extract", "step.bcrypt_check", "step.bcrypt_hash", "step.jwt_generate", "step.jwt_decode"},
				WiringHooks: []string{"agent.provider_registry", "ratchet.sse_route_registration", "ratchet.mcp_server_route_registration", "ratchet.db_init", "ratchet.auth_token", "ratchet.secrets_guard", "ratchet.provider_registry", "ratchet.tool_policy_engine", "ratchet.sub_agent_manager", "ratchet.tool_registry", "ratchet.container_manager", "ratchet.transcript_recorder", "ratchet.skill_manager", "ratchet.approval_manager", "ratchet.human_request_manager", "ratchet.webhook_manager", "ratchet.security_auditor", "ratchet.browser_manager", "ratchet.test_interaction"},
			},
		},
	}
}

// Capabilities returns the capability contracts for this plugin.
func (p *RatchetPlugin) Capabilities() []capability.Contract {
	return nil
}

// ModuleFactories returns the module factories registered by this plugin.
// "agent.provider" is registered here so ratchetplugin is self-contained and
// does not need workflow-plugin-agent loaded as a separate engine plugin
// (which would cause a duplicate step.agent_execute conflict).
func (p *RatchetPlugin) ModuleFactories() map[string]plugin.ModuleFactory {
	return map[string]plugin.ModuleFactory{
		"agent.provider":             agentplugin.NewProviderModuleFactory(),
		"ratchet.sse_hub":            newSSEHubFactory(),
		"ratchet.scheduler":          newSchedulerFactory(),
		"ratchet.mcp_client":         newMCPClientFactory(),
		"ratchet.mcp_server":         newMCPServerFactory(),
		"ratchet.tool_policy_engine": newToolPolicyModuleFactory(),
		"authz.casbin":               authz.NewCasbinModuleFactory(),
	}
}

// StepFactories returns the pipeline step factories registered by this plugin.
// step.agent_execute uses ratchet's richer implementation (browser, sub-agent,
// skill injection, etc.). step.provider_test and step.provider_models are
// delegated to the agent plugin's factories since ratchetplugin absorbs the
// agent plugin to avoid duplicate step registration.
func (p *RatchetPlugin) StepFactories() map[string]plugin.StepFactory {
	factories := map[string]plugin.StepFactory{
		"step.agent_execute":         newAgentExecuteStepFactory(),
		"step.provider_test":         agentplugin.NewProviderTestFactory(),
		"step.provider_models":       agentplugin.NewProviderModelsFactory(),
		"step.model_pull":            agentplugin.NewModelPullStepFactory(),
		"step.workspace_init":        newWorkspaceInitFactory(),
		"step.container_control":     newContainerControlFactory(),
		"step.secret_manage":         newSecretManageFactory(),
		"step.vault_config":          newVaultConfigFactory(),
		"step.mcp_reload":            newMCPReloadFactory(),
		"step.oauth_exchange":        newOAuthExchangeFactory(),
		"step.approval_resolve":      newApprovalResolveFactory(),
		"step.webhook_process":       newWebhookProcessStepFactory(),
		"step.security_audit":        newSecurityAuditFactory(),
		"step.test_interact":         newTestInteractFactory(),
		"step.human_request_resolve": newHumanRequestResolveFactory(),
		"step.memory_extract":        newMemoryExtractFactory(),
		"step.bcrypt_check":          newBcryptCheckFactory(),
		"step.bcrypt_hash":           newBcryptHashFactory(),
		"step.jwt_generate":          newJWTGenerateFactory(),
		"step.jwt_decode":            newJWTDecodeFactory(),
	}

	// Merge in authz step factories (step.authz_check_casbin, step.authz_add_policy, etc.)
	for k, v := range authz.StepFactories() {
		factories[k] = v
	}

	return factories
}

// WiringHooks returns the post-init wiring hooks for this plugin.
// agentplugin.ProviderRegistryHook() is included here because ratchetplugin
// absorbs the workflow-plugin-agent to avoid duplicate step type registration.
func (p *RatchetPlugin) WiringHooks() []plugin.WiringHook {
	return []plugin.WiringHook{
		agentplugin.ProviderRegistryHook(),
		sseRouteRegistrationHook(),
		mcpServerRouteHook(),
		dbInitHook(),
		authTokenHook(),
		secretsGuardHook(),
		providerRegistryHook(),
		toolPolicyEngineHook(),
		subAgentManagerHook(),
		toolRegistryHook(),
		containerManagerHook(),
		transcriptRecorderHook(),
		skillManagerHook(),
		approvalManagerHook(),
		humanRequestManagerHook(),
		webhookManagerHook(),
		securityAuditorHook(),
		browserManagerHook(),
		testInteractionHook(),
	}
}

// ModuleSchemas returns schema definitions for IDE completions and config validation.
func (p *RatchetPlugin) ModuleSchemas() []*schema.ModuleSchema {
	return []*schema.ModuleSchema{
		{
			Type:        "ratchet.sse_hub",
			Label:       "SSE Hub",
			Category:    "Realtime",
			Description: "Server-Sent Events hub for real-time dashboard updates.",
			ConfigFields: []schema.ConfigFieldDef{
				{Key: "path", Label: "Endpoint Path", Type: schema.FieldTypeString, DefaultValue: "/events", Description: "HTTP path for SSE connections"},
			},
			DefaultConfig: map[string]any{"path": "/events"},
		},
		{
			Type:        "ratchet.scheduler",
			Label:       "Scheduler",
			Category:    "Scheduling",
			Description: "Cron scheduler for periodic agent task polling.",
			ConfigFields: []schema.ConfigFieldDef{
				{Key: "cronExpression", Label: "Cron Expression", Type: schema.FieldTypeString, DefaultValue: "* * * * *", Description: "Standard cron expression for schedule interval"},
			},
			DefaultConfig: map[string]any{"cronExpression": "* * * * *"},
		},
		{
			Type:        "ratchet.mcp_client",
			Label:       "MCP Client",
			Category:    "Integration",
			Description: "Connects to external MCP servers via stdio and registers discovered tools.",
			ConfigFields: []schema.ConfigFieldDef{
				{Key: "servers", Label: "MCP Servers", Type: schema.FieldTypeJSON, Description: "Array of MCP server configs with name, command, and args"},
			},
		},
		{
			Type:        "ratchet.mcp_server",
			Label:       "MCP Server",
			Category:    "Integration",
			Description: "Exposes Ratchet APIs (agents, tasks, projects, messages) as MCP tools over HTTP.",
			ConfigFields: []schema.ConfigFieldDef{
				{Key: "path", Label: "Endpoint Path", Type: schema.FieldTypeString, DefaultValue: "/mcp", Description: "HTTP path for MCP JSON-RPC requests"},
			},
			DefaultConfig: map[string]any{"path": "/mcp"},
		},
	}
}

// secretsGuardHook creates a SecretGuard and registers it in the service registry.
// It defaults to vault-dev (managed HashiCorp Vault dev server).
// Backend selection priority:
//  1. data/vault-config.json (vault-remote or vault-dev)
//  2. Default: vault-dev
//  3. Fallback: FileProvider if vault binary is not available
//
// Also loads RATCHET_* environment variables for backward compatibility.
func secretsGuardHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.secrets_guard",
		Priority: 85,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			var sp secrets.Provider
			backendName := "vault-dev"

			// Check for saved vault config
			vcfg, _ := LoadVaultConfig(vaultConfigDir())

			if vcfg != nil && vcfg.Backend == "vault-remote" && vcfg.Address != "" && vcfg.Token != "" {
				// Use remote vault from saved config
				vp, err := secrets.NewVaultProvider(secrets.VaultConfig{
					Address:   vcfg.Address,
					Token:     vcfg.Token,
					MountPath: vcfg.MountPath,
					Namespace: vcfg.Namespace,
				})
				if err != nil {
					app.Logger().Warn("vault-remote config found but connection failed, falling back to vault-dev", "error", err)
				} else {
					sp = vp
					backendName = "vault-remote"
				}
			}

			// Default to vault-dev if no remote configured
			if sp == nil {
				dp, err := secrets.NewDevVaultProvider(secrets.DevVaultConfig{})
				if err != nil {
					app.Logger().Warn("vault-dev not available (vault binary not found), falling back to file provider", "error", err)
					sp = newFileProvider(app)
					backendName = "file"
				} else {
					sp = dp
					backendName = "vault-dev"
					_ = app.RegisterService("ratchet-vault-dev", dp)
				}
			}

			guard := NewSecretGuard(sp, backendName)

			ctx := context.Background()

			// Load all secrets from the provider
			_ = guard.LoadAllSecrets(ctx)

			// Register vault token for redaction if using remote vault
			if vcfg != nil && vcfg.Token != "" {
				guard.AddKnownSecret("VAULT_TOKEN", vcfg.Token)
			}

			// Backward compat: also load RATCHET_* env vars into SecretGuard
			// (These are loaded for redaction only; the env provider is not the primary store.)
			envProvider := secrets.NewEnvProvider("RATCHET_")
			for _, env := range os.Environ() {
				if strings.HasPrefix(env, "RATCHET_") {
					parts := strings.SplitN(env, "=", 2)
					name := strings.TrimPrefix(parts[0], "RATCHET_")
					if val, err := envProvider.Get(ctx, name); err == nil && val != "" {
						guard.AddKnownSecret(name, val)
					}
				}
			}

			app.Logger().Info("secrets backend initialized", "backend", backendName)

			// Register in service registry
			_ = app.RegisterService("ratchet-secret-guard", guard)
			return nil
		},
	}
}

// newFileProvider creates the default FileProvider for secrets storage.
func newFileProvider(app modular.Application) secrets.Provider {
	secretsDir := os.Getenv("RATCHET_SECRETS_DIR")
	if secretsDir == "" {
		secretsDir = "data/secrets"
	}
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		app.Logger().Warn("failed to create secrets dir", "error", err)
	}
	return secrets.NewFileProvider(secretsDir)
}

// providerRegistryHook creates a ProviderRegistry and registers it in the service registry.
func providerRegistryHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.provider_registry",
		Priority: 83,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			// Get DB
			var db *sql.DB
			if svc, ok := app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok {
					db = dbp.DB()
				}
			}
			if db == nil {
				return nil // no DB, skip
			}

			// Get secrets provider from SecretGuard
			var sp secrets.Provider
			if svc, ok := app.SvcRegistry()["ratchet-secret-guard"]; ok {
				if guard, ok := svc.(*SecretGuard); ok {
					sp = guard.Provider()
				}
			}

			registry := NewProviderRegistry(db, sp)
			_ = app.RegisterService("ratchet-provider-registry", registry)
			return nil
		},
	}
}

// toolRegistryHook creates a ToolRegistry with built-in tools and registers it.
func toolRegistryHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.tool_registry",
		Priority: 80,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			registry := NewToolRegistry()

			// Get DB for task/message tools
			var db *sql.DB
			if svc, ok := app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok {
					db = dbp.DB()
				}
			}

			// Wire policy engine if available
			if svc, ok := app.SvcRegistry()["ratchet-tool-policy-engine"]; ok {
				if pe, ok := svc.(*ToolPolicyEngine); ok {
					registry.SetPolicyEngine(pe)
				}
			}

			// Register built-in file and shell tools
			registry.Register(&tools.FileReadTool{})
			registry.Register(&tools.FileWriteTool{})
			registry.Register(&tools.FileListTool{})
			registry.Register(&tools.ShellExecTool{})
			registry.Register(&tools.WebFetchTool{})

			// Register git tools
			registry.Register(&tools.GitCloneTool{})
			registry.Register(&tools.GitStatusTool{})
			registry.Register(&tools.GitCommitTool{})
			registry.Register(&tools.GitPushTool{})
			registry.Register(&tools.GitDiffTool{})
			registry.Register(&tools.GitPRCreateTool{})

			if db != nil {
				registry.Register(&tools.TaskCreateTool{DB: db})
				registry.Register(&tools.TaskUpdateTool{DB: db})
				registry.Register(&tools.MessageSendTool{DB: db})
			}

			// Register approval tool with ApprovalManager (for SSE notifications)
			if svc, ok := app.SvcRegistry()["ratchet-approval-manager"]; ok {
				if am, ok := svc.(*ApprovalManager); ok {
					registry.Register(&tools.RequestApprovalTool{Manager: am})
				}
			}

			// Register human request tools if manager is available
			if svc, ok := app.SvcRegistry()["ratchet-human-request-manager"]; ok {
				if hrm, ok := svc.(*HumanRequestManager); ok {
					registry.Register(&tools.RequestHumanTool{Manager: hrm})
					registry.Register(&tools.CheckHumanRequestTool{Manager: hrm})
				}
			}

			// Register sub-agent tools if sub-agent manager is available
			if svc, ok := app.SvcRegistry()["ratchet-sub-agent-manager"]; ok {
				if mgr, ok := svc.(tools.SubAgentSpawner); ok {
					registry.Register(&tools.AgentSpawnTool{Manager: mgr})
					registry.Register(&tools.AgentCheckTool{Manager: mgr})
					registry.Register(&tools.AgentWaitTool{Manager: mgr})
				}
			}

			// Register memory tools if memory store is available
			if svc, ok := app.SvcRegistry()["ratchet-memory-store"]; ok {
				if ms, ok := svc.(*MemoryStore); ok {
					registry.Register(&tools.MemorySearchTool{Store: ms})
					registry.Register(&tools.MemorySaveTool{Store: ms})
				}
			}

			// Register browser tools if browser manager is available
			if svc, ok := app.SvcRegistry()["ratchet-browser-manager"]; ok {
				if bm, ok := svc.(*BrowserManager); ok {
					registry.Register(&tools.BrowserNavigateTool{Manager: bm})
					registry.Register(&tools.BrowserScreenshotTool{Manager: bm})
					registry.Register(&tools.BrowserClickTool{Manager: bm})
					registry.Register(&tools.BrowserExtractTool{Manager: bm})
					registry.Register(&tools.BrowserFillTool{Manager: bm})
				}
			}

			// Development tools
			registry.Register(&tools.CodeReviewTool{})
			registry.Register(&tools.CodeComplexityTool{})
			registry.Register(&tools.CodeDiffReviewTool{})
			registry.Register(&tools.GitLogStatsTool{})
			registry.Register(&tools.TestCoverageTool{})

			// Security tools
			registry.Register(&tools.VulnCheckTool{})
			registry.Register(&tools.SecurityScanURLTool{})
			if db != nil {
				runAudit := func(ctx context.Context) (map[string]any, error) {
					auditor := NewSecurityAuditor(db, app)
					report := auditor.RunAll(ctx)
					findings := make([]map[string]any, 0, len(report.Findings))
					for _, f := range report.Findings {
						findings = append(findings, map[string]any{
							"check":       f.Check,
							"severity":    string(f.Severity),
							"title":       f.Title,
							"description": f.Description,
							"remediation": f.Remediation,
						})
					}
					summary := map[string]int{}
					for sev, count := range report.Summary {
						summary[string(sev)] = count
					}
					passedCount := 12 - len(report.Findings)
					if passedCount < 0 {
						passedCount = 0
					}
					return map[string]any{
						"score":        report.Score,
						"summary":      summary,
						"findings":     findings,
						"passed_count": passedCount,
						"failed_count": len(report.Findings),
					}, nil
				}
				registry.Register(&tools.SecurityScanTool{RunAudit: runAudit})
				registry.Register(&tools.ComplianceReportTool{RunAudit: runAudit})
				registry.Register(&tools.SecretAuditTool{DB: db})
			}

			// Data tools
			registry.Register(&tools.DBQueryExternalTool{})
			if db != nil {
				registry.Register(&tools.DBAnalyzeTool{DB: db})
				registry.Register(&tools.DBHealthCheckTool{DB: db})
				registry.Register(&tools.SchemaInspectTool{DB: db})
				registry.Register(&tools.DataProfileTool{DB: db})
			}

			// Register k8s operations tools (shell out to kubectl)
			registry.Register(&tools.K8sGetPodsTool{})
			registry.Register(&tools.K8sGetEventsTool{})
			registry.Register(&tools.K8sGetLogsTool{})
			registry.Register(&tools.K8sDescribeTool{})
			registry.Register(&tools.K8sRestartPodTool{})
			registry.Register(&tools.K8sScaleTool{})
			registry.Register(&tools.K8sRollbackTool{})
			registry.Register(&tools.K8sApplyTool{})
			registry.Register(&tools.InfraHealthCheckTool{})
			registry.Register(&tools.DeploymentStatusTool{})
			registry.Register(&tools.K8sTopTool{})

			// Register in service registry
			_ = app.RegisterService("ratchet-tool-registry", registry)
			return nil
		},
	}
}

// containerManagerHook creates a ContainerManager and registers it in the service registry.
func containerManagerHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.container_manager",
		Priority: 82,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			var db *sql.DB
			if svc, ok := app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok {
					db = dbp.DB()
				}
			}
			cm := NewContainerManager(db)
			_ = app.RegisterService("ratchet-container-manager", cm)
			return nil
		},
	}
}

// transcriptRecorderHook creates a TranscriptRecorder and registers it.
func transcriptRecorderHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.transcript_recorder",
		Priority: 75,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			// Get DB
			var db *sql.DB
			if svc, ok := app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok {
					db = dbp.DB()
				}
			}
			if db == nil {
				return nil // no DB, skip
			}

			// Get SecretGuard (optional)
			var guard *SecretGuard
			if svc, ok := app.SvcRegistry()["ratchet-secret-guard"]; ok {
				guard, _ = svc.(*SecretGuard)
			}

			recorder := NewTranscriptRecorder(db, guard)
			_ = app.RegisterService("ratchet-transcript-recorder", recorder)
			return nil
		},
	}
}

// approvalManagerHook creates an ApprovalManager and registers it in the service registry.
func approvalManagerHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.approval_manager",
		Priority: 81,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			var db *sql.DB
			if svc, ok := app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok {
					db = dbp.DB()
				}
			}
			if db == nil {
				return nil // no DB, skip
			}

			am := NewApprovalManager(db)

			// Wire in SSE hub if available (optional, for push notifications)
			for _, svc := range app.SvcRegistry() {
				if hub, ok := svc.(*SSEHub); ok {
					am.SetSSEHub(hub)
					break
				}
			}

			_ = app.RegisterService("ratchet-approval-manager", am)
			return nil
		},
	}
}

// humanRequestManagerHook creates a HumanRequestManager and registers it in the service registry.
func humanRequestManagerHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.human_request_manager",
		Priority: 81,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			var db *sql.DB
			if svc, ok := app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok {
					db = dbp.DB()
				}
			}
			if db == nil {
				return nil
			}
			hrm := NewHumanRequestManager(db)
			for _, svc := range app.SvcRegistry() {
				if hub, ok := svc.(*SSEHub); ok {
					hrm.SetSSEHub(hub)
					break
				}
			}
			_ = app.RegisterService("ratchet-human-request-manager", hrm)
			return nil
		},
	}
}

// webhookManagerHook creates a WebhookManager and registers it in the service registry.
func webhookManagerHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.webhook_manager",
		Priority: 73,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			var db *sql.DB
			if svc, ok := app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok {
					db = dbp.DB()
				}
			}
			if db == nil {
				return nil // no DB, skip
			}

			var guard *SecretGuard
			if svc, ok := app.SvcRegistry()["ratchet-secret-guard"]; ok {
				guard, _ = svc.(*SecretGuard)
			}

			wm := NewWebhookManager(db, guard)
			_ = app.RegisterService("ratchet-webhook-manager", wm)
			return nil
		},
	}
}

// subAgentManagerHook creates a SubAgentManager and registers it in the service registry.
func subAgentManagerHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.sub_agent_manager",
		Priority: 79,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			var db *sql.DB
			if svc, ok := app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok {
					db = dbp.DB()
				}
			}
			if db == nil {
				return nil // no DB, skip
			}
			mgr := NewSubAgentManager(db, 0, 0)
			_ = app.RegisterService("ratchet-sub-agent-manager", mgr)
			return nil
		},
	}
}

// testInteractionHook wires the HTTPSource from a test provider into the
// service registry and connects it to the SSE hub for push notifications.
// This runs at low priority so all other services are already available.
//
// Handles "ratchet.ai_provider" modules (legacy). For the new "agent.provider"
// type from workflow-plugin-agent, the HTTPSource wiring is handled separately
// via the agent plugin's own mechanisms.
func testInteractionHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.test_interaction",
		Priority: 50,
		Hook: func(app modular.Application, cfg *config.WorkflowConfig) error {
			if cfg == nil {
				return nil
			}
			for _, modCfg := range cfg.Modules {
				// Handle legacy ratchet.ai_provider modules only.
				// agent.provider modules from the agent plugin are handled differently.
				if modCfg.Type == "agent.provider" {
					// For agent.provider, check if there's a ProviderModule with a
					// ratchet-compatible Provider we can wire into the ProviderRegistry.
					svc, ok := app.SvcRegistry()[modCfg.Name]
					if !ok {
						continue
					}
					agentMod, ok := svc.(*agentplugin.ProviderModule)
					if !ok {
						continue
					}
					testProvider := agentMod.Provider()
					if testProvider == nil {
						continue
					}
					// Check if provider config is "test" mode — only override registry for test providers.
					if provType, _ := modCfg.Config["provider"].(string); provType != "test" {
						continue
					}
					if regSvc, ok := app.SvcRegistry()["ratchet-provider-registry"]; ok {
						if registry, ok := regSvc.(*ProviderRegistry); ok {
							registry.factories["test"] = func(_ string, _ LLMProviderConfig) (provider.Provider, error) {
								return testProvider, nil
							}
							if registry.db != nil {
								_, _ = registry.db.Exec(`UPDATE llm_providers SET type = 'test', alias = 'test' WHERE id = 'mock-default'`)
								registry.InvalidateCache()
							}
							app.Logger().Info("test interaction hook: registered agent.provider test factory in ratchet provider registry")
						}
					}
					continue
				}

				if modCfg.Type != "ratchet.ai_provider" {
					continue
				}
				svc, ok := app.SvcRegistry()[modCfg.Name]
				if !ok {
					continue
				}
				providerMod, ok := svc.(*AIProviderModule)
				if !ok {
					continue
				}
				httpSource := providerMod.TestHTTPSource()
				if httpSource == nil {
					continue
				}
				// Wire SSE hub
				for _, svc := range app.SvcRegistry() {
					if hub, ok := svc.(*SSEHub); ok {
						httpSource.SetSSEHub(hub)
						break
					}
				}
				// Register HTTPSource so step.test_interact can find it
				_ = app.RegisterService("ratchet-test-http-source", httpSource)

				// Override the default provider in the ProviderRegistry so that
				// step.agent_execute uses the test provider instead of the seeded
				// mock provider from the llm_providers table.
				testProvider := providerMod.Provider()
				if regSvc, ok := app.SvcRegistry()["ratchet-provider-registry"]; ok {
					if registry, ok := regSvc.(*ProviderRegistry); ok {
						// Register a "test" factory that returns our pre-built test provider
						registry.factories["test"] = func(_ string, _ LLMProviderConfig) (provider.Provider, error) {
							return testProvider, nil
						}
						// Update the default provider row in the DB from "mock" to "test"
						if registry.db != nil {
							_, _ = registry.db.Exec(`UPDATE llm_providers SET type = 'test', alias = 'test' WHERE id = 'mock-default'`)
							registry.InvalidateCache()
						}
						app.Logger().Info("test interaction hook: registered test provider factory and updated default provider")
					}
				}

				app.Logger().Info("test interaction hook: registered HTTPSource for test provider")
			}
			return nil
		},
	}
}
