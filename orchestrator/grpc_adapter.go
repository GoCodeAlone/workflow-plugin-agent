package orchestrator

// Union gRPC adapter — the first in-process→sdk bridge in the org (P2b-T3 / PR2).
//
// ADR 0053 (orchestrator unconditional fold-in) serves the orchestrator's steps
// UNDER THE AGENT PLUGIN NAME via the workflow-plugin-agent gRPC binary. The
// agent plugin (workflow-plugin-agent root pkg) is already sdk-native: it
// implements sdk.{PluginProvider, StepProvider, ModuleProvider, TypedStepProvider,
// TypedModuleProvider} and exposes its 4 step types + agent.provider module via
// the gRPC legacy-bridge (typed_contracts.go: AgentPlugin.CreateStep ->
// factory(name, config, nil) wrapped in legacyStepInstance).
//
// The orchestrator plugin (RatchetPlugin) is IN-PROCESS only (plugin.EnginePlugin);
// its step factories are reachable only through RatchetPlugin.StepFactories()
// (or the package-level new*Factory() constructors those map values call).
// agent does NOT import orchestrator, but orchestrator imports agent — so a
// union adapter living in package orchestrator can reference BOTH without an
// import cycle. (You cannot move the orchestrator factories INTO
// agent.StepFactories() because the reverse import would cycle.)
//
// This adapter serves the orchestrator's 3 STATELESS step types — the subset
// PR1 (P2b-T2) proved safe under app=nil (the bridge contract): the factory is
// invoked as factory(name, config, nil) and every Execute path that reaches
// app.SvcRegistry() is guarded or in a nil-safe branch. The 23 stateful
// orchestrator step types are intentionally EXCLUDED — they require
// modular.Application and would panic or no-op under app=nil.
//
// Served union (7 types, all under Manifest().Name == "workflow-plugin-agent"):
//
//	agent (4):     step.agent_execute, step.provider_test,
//	               step.provider_models, step.model_pull
//	orchestrator (3, stateless only):
//	               step.lsp_diagnose,
//	               step.self_improve_validate,
//	               step.self_improve_diff
//
// Dispatch: CreateStep routes agent-owned types to AgentPlugin.CreateStep
// (preserving its typed/legacy split) and orchestrator-owned types to the local
// factory + the shared agent.NewLegacyStepInstance wrap. Modules and Typed*
// step types delegate ENTIRELY to the agent plugin: the orchestrator contributes
// no modules or typed (proto) contracts to the union — only legacy stateless steps.
//
// ADDITIVE: this file is NOT wired into cmd/workflow-plugin-agent/main.go (that
// is PR4/T5) and does NOT modify plugin.json, plugin.contracts.json, or the
// proto. PR2 = adapter + test only.

import (
	"context"
	"fmt"

	agentplugin "github.com/GoCodeAlone/workflow-plugin-agent"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"google.golang.org/protobuf/types/known/anypb"
)

// pipelineExecutor is the (context.Context, *module.PipelineContext) Execute
// signature the agent's legacyStepInstance wraps (typed_contracts.go). The 3
// served orchestrator step types all satisfy it (compile-time-asserted in
// stateless_nilapp_test.go). Re-declared here so CreateStep can assert the
// factory output matches before wrapping — mirroring AgentPlugin.CreateStep.
type pipelineExecutor interface {
	Execute(context.Context, *module.PipelineContext) (*module.StepResult, error)
}

// statelessStepTypes is the orchestrator subset served under the fold-in: the 3
// steps PR1 proved safe under app=nil. Stateful orchestrator steps (workspace_init,
// approval_resolve, blackboard_*, self_improve_deploy, ...) are intentionally NOT
// served — they need modular.Application.
var statelessStepTypes = []string{
	"step.lsp_diagnose",
	"step.self_improve_validate",
	"step.self_improve_diff",
}

// UnionAdapter exposes the agent plugin's 4 steps + the orchestrator's 3
// stateless steps = 7 under the AGENT plugin name, via the sdk gRPC interfaces.
// It delegates Manifest/Module/Typed* to the embedded *agent.AgentPlugin and
// dispatches CreateStep across both factory maps.
//
// It implements sdk.PluginProvider, sdk.StepProvider, sdk.ModuleProvider,
// sdk.TypedStepProvider, sdk.TypedModuleProvider (compile-time-asserted in
// grpc_adapter_test.go).
type UnionAdapter struct {
	agent *agentplugin.AgentPlugin
	// statelessFactories maps the 3 served orchestrator step types to their
	// plugin.StepFactory (the same funcs RatchetPlugin.StepFactories() returns).
	// Each is invoked as factory(name, config, nil) — app=nil, PR1-proven safe.
	statelessFactories map[string]plugin.StepFactory
}

// NewUnionAdapter builds a UnionAdapter over a fresh agent plugin and the 3
// orchestrator stateless step factories. The factories are obtained from the
// package-level constructors (the same ones RatchetPlugin.StepFactories() calls)
// rather than constructing a full RatchetPlugin, because the adapter serves
// ONLY these 3 types and must not pull in the orchestrator's stateful wiring.
func NewUnionAdapter() *UnionAdapter {
	return &UnionAdapter{
		agent: agentplugin.New(),
		statelessFactories: map[string]plugin.StepFactory{
			"step.lsp_diagnose":          newLSPDiagnoseFactory(),
			"step.self_improve_validate": newSelfImproveValidateFactory(),
			"step.self_improve_diff":     newSelfImproveDiffFactory(),
		},
	}
}

// Manifest implements sdk.PluginProvider. Delegates to the agent plugin so the
// union is served under the AGENT name+version (the ADR 0053 fold-in), not ratchet.
func (u *UnionAdapter) Manifest() sdk.PluginManifest {
	return u.agent.Manifest()
}

// StepTypes implements sdk.StepProvider: the 7-type union (agent's 4 + the 3
// orchestrator stateless). Deduped defensively in case agent ever absorbs a
// stateless type; ordering is agent-types-first then stateless (stable).
func (u *UnionAdapter) StepTypes() []string {
	seen := make(map[string]struct{}, 7)
	out := make([]string, 0, 7)
	for _, t := range u.agent.StepTypes() {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for _, t := range statelessStepTypes {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// CreateStep implements sdk.StepProvider. Agent-owned types delegate to
// AgentPlugin.CreateStep (preserving its typed/legacy split + error shape).
// Orchestrator-owned stateless types call the local factory with app=nil (the
// PR1-proven-safe bridge contract) and wrap the result in the shared
// agent.NewLegacyStepInstance. Unknown types — including orchestrator-only
// STATEFUL types like step.self_improve_deploy — return an error.
func (u *UnionAdapter) CreateStep(typeName, name string, config map[string]any) (sdk.StepInstance, error) {
	// Agent-owned: delegate wholesale (covers all 4 agent step types). Using the
	// agent's own StepTypes() as the membership test keeps the dispatch source of
	// truth single-sided and survives future agent-side additions.
	for _, t := range u.agent.StepTypes() {
		if t == typeName {
			return u.agent.CreateStep(typeName, name, config)
		}
	}

	// Orchestrator-owned stateless: invoke factory(name, config, nil).
	factory, ok := u.statelessFactories[typeName]
	if !ok {
		return nil, fmt.Errorf("union adapter: unknown or non-served step type %q (only the 7-type agent+stateless union is served)", typeName)
	}
	step, err := factory(name, config, nil)
	if err != nil {
		return nil, fmt.Errorf("union adapter: orchestrator factory %q: %w", typeName, err)
	}
	// The factory output must implement the module.PipelineStep Execute signature
	// that agent.NewLegacyStepInstance wraps — the same assertion
	// AgentPlugin.CreateStep performs. The 3 stateless steps satisfy this
	// (compile-time-asserted in stateless_nilapp_test.go).
	pipelineStep, ok := step.(pipelineExecutor)
	if !ok {
		return nil, fmt.Errorf("union adapter: orchestrator factory %q returned %T not satisfying the pipeline Execute signature", typeName, step)
	}
	return agentplugin.NewLegacyStepInstance(pipelineStep), nil
}

// ModuleTypes implements sdk.ModuleProvider — delegates to the agent plugin
// (agent.provider). The orchestrator contributes no modules to the union.
func (u *UnionAdapter) ModuleTypes() []string {
	return u.agent.ModuleTypes()
}

// CreateModule implements sdk.ModuleProvider — delegates to the agent plugin.
func (u *UnionAdapter) CreateModule(typeName, name string, config map[string]any) (sdk.ModuleInstance, error) {
	return u.agent.CreateModule(typeName, name, config)
}

// TypedStepTypes implements sdk.TypedStepProvider — the agent's typed set only.
// The 3 orchestrator stateless steps are legacy-served (CreateStep), NOT typed:
// they carry no proto contracts in this adapter.
func (u *UnionAdapter) TypedStepTypes() []string {
	return u.agent.TypedStepTypes()
}

// CreateTypedStep implements sdk.TypedStepProvider — delegates to the agent
// plugin's typed factories (provider_models, model_pull).
func (u *UnionAdapter) CreateTypedStep(typeName, name string, config *anypb.Any) (sdk.StepInstance, error) {
	return u.agent.CreateTypedStep(typeName, name, config)
}

// TypedModuleTypes implements sdk.TypedModuleProvider — delegates to the agent
// plugin (agent.provider).
func (u *UnionAdapter) TypedModuleTypes() []string {
	return u.agent.TypedModuleTypes()
}

// CreateTypedModule implements sdk.TypedModuleProvider — delegates to the agent
// plugin's typed module factory (agent.provider).
func (u *UnionAdapter) CreateTypedModule(typeName, name string, config *anypb.Any) (sdk.ModuleInstance, error) {
	return u.agent.CreateTypedModule(typeName, name, config)
}
