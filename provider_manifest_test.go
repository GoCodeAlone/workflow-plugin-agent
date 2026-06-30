package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

// pluginJSONCapabilities is the subset of plugin.json these drift-gate tests
// assert against.
type pluginJSONCapabilities struct {
	Capabilities struct {
		ModuleTypes []string `json:"moduleTypes"`
		StepTypes   []string `json:"stepTypes"`
	} `json:"capabilities"`
}

func loadPluginJSONCapabilities(t *testing.T) pluginJSONCapabilities {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var manifest pluginJSONCapabilities
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	return manifest
}

// TestAgentPluginSatisfiesSDKProviderInterfaces asserts that *AgentPlugin — the
// value handed to sdk.ServePluginFull in cmd/workflow-plugin-agent/main.go —
// implements every sdk provider interface the gRPC server consults at runtime.
// This is the type-level contract for the distributable binary: if any of these
// assertions break, the binary will fail to register capabilities with the host.
func TestAgentPluginSatisfiesSDKProviderInterfaces(t *testing.T) {
	p := New()

	if _, ok := any(p).(sdk.PluginProvider); !ok {
		t.Fatal("expected *AgentPlugin to satisfy sdk.PluginProvider (Manifest)")
	}
	if _, ok := any(p).(sdk.ModuleProvider); !ok {
		t.Fatal("expected *AgentPlugin to satisfy sdk.ModuleProvider")
	}
	if _, ok := any(p).(sdk.StepProvider); !ok {
		t.Fatal("expected *AgentPlugin to satisfy sdk.StepProvider")
	}
	if _, ok := any(p).(sdk.TypedModuleProvider); !ok {
		t.Fatal("expected *AgentPlugin to satisfy sdk.TypedModuleProvider")
	}
	if _, ok := any(p).(sdk.TypedStepProvider); !ok {
		t.Fatal("expected *AgentPlugin to satisfy sdk.TypedStepProvider")
	}
	if _, ok := any(p).(sdk.ContractProvider); !ok {
		t.Fatal("expected *AgentPlugin to satisfy sdk.ContractProvider")
	}
}

// TestAgentPluginProviderManifestRuntimeTruth asserts the runtime provider
// returns the manifest the gRPC GetManifest handler will advertise to the host:
// canonical name, and module/step capability lists matching what the engine
// will later enumerate via ModuleTypes()/StepTypes(). This is the P6 gate that
// proves the provider (and thus the binary) agrees with the reconciled
// plugin.json — the same assertion wfctl verify-capabilities makes at runtime.
func TestAgentPluginProviderManifestRuntimeTruth(t *testing.T) {
	p := New()

	provider, ok := any(p).(sdk.PluginProvider)
	if !ok {
		t.Fatal("expected *AgentPlugin to satisfy sdk.PluginProvider")
	}
	m := provider.Manifest()
	if m.Name != "workflow-plugin-agent" {
		t.Fatalf("Manifest().Name = %q, want workflow-plugin-agent", m.Name)
	}
	if m.Author != "GoCodeAlone" {
		t.Fatalf("Manifest().Author = %q, want GoCodeAlone", m.Author)
	}
	if m.Description == "" {
		t.Fatal("Manifest().Description is empty")
	}

	wantModule := map[string]bool{"agent.provider": true}
	for _, mt := range p.ModuleTypes() {
		if !wantModule[mt] {
			t.Fatalf("unexpected ModuleType %q", mt)
		}
		delete(wantModule, mt)
	}
	if len(wantModule) > 0 {
		t.Fatalf("ModuleTypes() missing %v", wantModule)
	}

	wantSteps := map[string]bool{
		"step.agent_execute":   true,
		"step.provider_test":   true,
		"step.provider_models": true,
		"step.model_pull":      true,
	}
	gotSteps := p.StepTypes()
	if len(gotSteps) != len(wantSteps) {
		t.Fatalf("StepTypes() = %v (len %d), want %v (len %d)", gotSteps, len(gotSteps), wantSteps, len(wantSteps))
	}
	for _, s := range gotSteps {
		if !wantSteps[s] {
			t.Fatalf("unexpected StepType %q", s)
		}
		delete(wantSteps, s)
	}
	if len(wantSteps) > 0 {
		t.Fatalf("StepTypes() missing %v", wantSteps)
	}
}

// TestPluginJSONStepTypesMatchRuntimeTruth is the P6 drift gate. It enforces
// TWO invariants that together prevent plugin.json ↔ runtime divergence:
//
//  1. Every runtime StepTypes() entry MUST be declared in plugin.json
//     (runtime ⊆ declared). This catches a step the binary serves but never
//     advertises — the engine would reject it at load.
//  2. Every plugin.json-declared stepType MUST carry a strict contract
//     descriptor in plugin.contracts.json (declared ⊆ descriptors). This is
//     the strict-contracts coverage gate: the contract SURFACE must be complete
//     even while the gRPC SERVING surface catches up.
//
// During the Phase 2b contracts-first transition (PR3), plugin.json advertises
// the 7-type union (4 agent + 3 stateless orchestrator) and all 7 carry strict
// descriptors, but runtime StepTypes() still returns only the agent's 4 — the
// orchestrator union adapter (PR2) is wired into the gRPC binary's StepTypes()
// in PR4. `wfctl plugin verify-capabilities` compares ONLY Name+Version (not
// stepTypes), so this intermediate is not rejected by tooling; the full 7-step
// runtime parity is restored in PR4 and asserted by TestStepTypesUnionServed
// (added there). This test intentionally does NOT assert declared ⊆ runtime
// during the transition — only runtime ⊆ declared + declared ⊆ descriptors.
func TestPluginJSONStepTypesMatchRuntimeTruth(t *testing.T) {
	manifest := loadPluginJSONCapabilities(t)
	if len(manifest.Capabilities.StepTypes) == 0 {
		t.Fatal("plugin.json capabilities.stepTypes is empty")
	}

	runtimeSet := map[string]bool{}
	for _, s := range New().StepTypes() {
		runtimeSet[s] = true
	}
	declared := map[string]bool{}
	for _, s := range manifest.Capabilities.StepTypes {
		declared[s] = true
	}

	// Invariant 1: every served step MUST be advertised (runtime ⊆ declared).
	for s := range runtimeSet {
		if !declared[s] {
			t.Fatalf("plugin.json stepTypes missing runtime step %q (drift: plugin.json must list every StepTypes() entry)", s)
		}
	}

	// Invariant 2: every advertised stepType MUST have a strict contract
	// descriptor (declared ⊆ descriptors) — the strict-contracts coverage gate.
	registry := New().ContractRegistry()
	descriptorByStep := map[string]bool{}
	for _, d := range registry.Contracts {
		if d.Kind == pb.ContractKind_CONTRACT_KIND_STEP {
			descriptorByStep[d.StepType] = true
		}
	}
	for s := range declared {
		if !descriptorByStep[s] {
			t.Fatalf("plugin.json stepTypes declares %q but no strict contract descriptor exists in plugin.contracts.json (strict-contracts coverage gate)", s)
		}
	}

	if len(manifest.Capabilities.ModuleTypes) == 0 {
		t.Fatal("plugin.json capabilities.moduleTypes is empty")
	}
	runtimeMods := map[string]bool{}
	for _, m := range New().ModuleTypes() {
		runtimeMods[m] = true
	}
	for _, m := range manifest.Capabilities.ModuleTypes {
		if !runtimeMods[m] {
			t.Fatalf("plugin.json moduleTypes declares %q but runtime ModuleTypes() does not return it", m)
		}
	}
}

// TestPluginJSONDeclaresSevenStepTypeUnion pins the Phase 2b contracts-first
// advertisement: plugin.json capabilities.stepTypes lists the 7-type union (4
// agent + 3 stateless orchestrator) in a stable canonical order. The orchestrator
// trio is served by the union adapter wired in PR4; the descriptors + plugin.json
// advertisement land here (PR3) so strict-contracts coverage is complete before
// the serving surface expands.
func TestPluginJSONDeclaresSevenStepTypeUnion(t *testing.T) {
	manifest := loadPluginJSONCapabilities(t)
	got := append([]string(nil), manifest.Capabilities.StepTypes...)
	sort.Strings(got)
	want := []string{
		"step.agent_execute",
		"step.lsp_diagnose",
		"step.model_pull",
		"step.provider_models",
		"step.provider_test",
		"step.self_improve_diff",
		"step.self_improve_validate",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("plugin.json stepTypes = %v (len %d), want %v (len %d)", manifest.Capabilities.StepTypes, len(got), want, len(want))
	}
	for i, s := range want {
		if got[i] != s {
			t.Fatalf("plugin.json stepTypes = %v, want %v", manifest.Capabilities.StepTypes, want)
		}
	}
}
