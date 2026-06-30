package orchestrator

// Transitive-call audit for the 3 stateless steps under app=nil (P2b-T2, D1/D10).
//
// The gRPC legacy-bridge path (typed_contracts.go AgentPlugin.CreateStep ->
// factory(name, config, nil)) constructs each step with app=nil. For a step to
// be serve-able it must tolerate app=nil: every path from Execute that reaches
// app.SvcRegistry() / s.app.* — DIRECTLY or TRANSITIVELY through helpers in
// services.go / step_agent_execute.go — must be nil-safe (guarded, behind an
// app!=nil check, or in a guarded branch). A flat grep on the step files is
// INSUFFICIENT: step_self_improve_diff has zero direct s.app derefs yet still
// panics transitively via resolveServices.
//
// Audit (verified against this worktree's source; line numbers are current):
//
//	step.lsp_diagnose  (step_lsp_diagnose.go)
//	  Execute:47  -> s.findLSPProvider() -> lookupLSPProvider(s.app)  (:103->:107)
//	    lookupLSPProvider:108  `if app == nil { return nil }`        -> GUARDED ✓
//	  (no other s.app deref in the file)
//	  DISPOSITION: already nil-safe.
//
//	step.self_improve_validate  (step_self_improve_validate.go)
//	  Execute:86  -> lookupLSPProvider(s.app)                         -> GUARDED ✓ (above)
//	  Execute:79  (only when currentYAML != "") -> s.checkImmutability
//	    checkImmutability:121 -> findGuardrailsModule(s.app)
//	      findGuardrailsModule (step_agent_execute.go:733)
//	        :734  `for _, svc := range app.SvcRegistry()`             -> UNGUARDED ✗
//	  Execute:104 -> s.runMCPValidation
//	    runMCPValidation:152  `if s.app == nil { return ... }`        -> GUARDED ✓ (pre-check)
//	  (Execute itself never directly derefs s.app)
//	  DISPOSITION: needs `if app == nil { return nil }` guard on findGuardrailsModule.
//
//	step.self_improve_diff  (step_self_improve_diff.go)
//	  Execute:102 (only when outputToBlackboard && hasChanges) -> s.postToBlackboard
//	    postToBlackboard:118 -> resolveServices(s.app).Blackboard
//	      resolveServices (services.go:508)
//	        :509  `reg := app.SvcRegistry()`                          -> UNGUARDED ✗
//	    postToBlackboard:119 `if IsNull(bb) { return err }` runs AFTER resolveServices,
//	      so it cannot save us from the :509 panic.
//	  (the step file has NO direct s.app deref — flat grep is false-clean)
//	  DISPOSITION: needs `if app == nil { return <null bundle> }` guard at the TOP of
//	    resolveServices, BEFORE the `reg := app.SvcRegistry()` line.
//
// After the two guards above are applied, all three steps are nil-app-safe.

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// TestStatelessNilApp proves the 3 served (stateless) steps tolerate app=nil,
// the condition the gRPC legacy-bridge imposes (factory(name, config, nil)).
// Each subtest constructs the step via its real plugin.StepFactory with app=nil
// — mirroring AgentPlugin.CreateStep's call — and asserts Execute returns a
// graceful result rather than panicking.
func TestStatelessNilApp(t *testing.T) {
	t.Run("lsp_diagnose", func(t *testing.T) {
		// Factory mirrors AgentPlugin.CreateStep: factory(name, config, nil).
		factory := newLSPDiagnoseFactory()
		stepAny, err := factory("step.lsp_diagnose", map[string]any{}, nil)
		if err != nil {
			t.Fatalf("factory returned error: %v", err)
		}
		step := stepAny.(*LSPDiagnoseStep)
		if step.app != nil {
			t.Fatalf("expected app==nil on the constructed step (bridge contract)")
		}

		// Provide yaml content so the step reaches the lookupLSPProvider path
		// (the app=nil hazard) rather than the early empty-content return.
		pc := &module.PipelineContext{Current: map[string]any{
			"yaml": "name: test\n",
		}}

		result, err := step.Execute(context.Background(), pc)
		if err != nil {
			t.Fatalf("Execute with app=nil returned error (expected graceful): %v", err)
		}
		if result == nil {
			t.Fatal("Execute returned nil result")
		}
		// lookupLSPProvider(nil) returns nil -> step returns empty diagnostics.
		count, _ := result.Output["count"].(int)
		if count != 0 {
			t.Errorf("expected 0 diagnostics (no provider when app=nil), got %d", count)
		}
	})

	t.Run("self_improve_validate", func(t *testing.T) {
		// Factory mirrors AgentPlugin.CreateStep: factory(name, config, nil).
		factory := newSelfImproveValidateFactory()
		stepAny, err := factory("step.self_improve_validate", map[string]any{}, nil)
		if err != nil {
			t.Fatalf("factory returned error: %v", err)
		}
		step := stepAny.(*SelfImproveValidateStep)
		if step.app != nil {
			t.Fatalf("expected app==nil on the constructed step (bridge contract)")
		}

		// Provide BOTH proposed + current YAML so checkImmutability runs (the
		// branch that hits findGuardrailsModule(s.app) — the app=nil hazard).
		pc := &module.PipelineContext{Current: map[string]any{
			"proposed_yaml": "name: foo\nversion: \"2\"\n",
			"current_yaml":  "name: bar\nversion: \"1\"\n",
		}}

		result, err := step.Execute(context.Background(), pc)
		if err != nil {
			t.Fatalf("Execute with app=nil returned error (expected graceful): %v", err)
		}
		if result == nil {
			t.Fatal("Execute returned nil result")
		}
		// The YAML-schema path runs pre-checkImmutability and is app-free, so
		// `valid`/`errors` must still be computed. With well-formed YAML and
		// no guardrails module (findGuardrailsModule(nil)->nil), valid==true.
		valid, _ := result.Output["valid"].(bool)
		if !valid {
			t.Errorf("expected valid=true (well-formed YAML, no guardrails under app=nil), got valid=%v, output=%v", valid, result.Output)
		}
		errs, _ := result.Output["errors"].([]string)
		if errs == nil {
			t.Errorf("expected errors slice to be present (computed pre-checkImmutability), got nil; output=%v", result.Output)
		}
	})

	t.Run("self_improve_diff", func(t *testing.T) {
		// Factory mirrors AgentPlugin.CreateStep: factory(name, config, nil).
		// output_to_blackboard=true forces the postToBlackboard path that
		// transitively hits resolveServices(s.app) — the app=nil hazard.
		factory := newSelfImproveDiffFactory()
		stepAny, err := factory("step.self_improve_diff", map[string]any{
			"output_to_blackboard": true,
		}, nil)
		if err != nil {
			t.Fatalf("factory returned error: %v", err)
		}
		step := stepAny.(*SelfImproveDiffStep)
		if step.app != nil {
			t.Fatalf("expected app==nil on the constructed step (bridge contract)")
		}

		// Differing proposed/current so hasChanges=true -> postToBlackboard runs.
		pc := &module.PipelineContext{Current: map[string]any{
			"proposed_yaml": "name: foo\n",
			"current_yaml":  "name: bar\n",
		}}

		result, err := step.Execute(context.Background(), pc)
		if err != nil {
			t.Fatalf("Execute with app=nil returned error (expected graceful): %v", err)
		}
		if result == nil {
			t.Fatal("Execute returned nil result")
		}
		// The diff itself is computed app-free; only the Blackboard post is
		// gated. resolveServices(nil) returns a null bundle whose Blackboard
		// IsNull()s -> postToBlackboard records a non-fatal warning instead of
		// panicking. The diff output must survive.
		diff, _ := result.Output["diff"].(string)
		if diff == "" {
			t.Errorf("expected non-empty diff (content differs), got empty; output=%v", result.Output)
		}
		hasChanges, _ := result.Output["has_changes"].(bool)
		if !hasChanges {
			t.Errorf("expected has_changes=true, got false; output=%v", result.Output)
		}
	})
}

// Compile-time guard: the 3 served steps' factories are plugin.StepFactory
// (the type AgentPlugin.CreateStep dispatches through). If a factory signature
// drifts, this fails to compile.
var _ plugin.StepFactory = newLSPDiagnoseFactory()
var _ plugin.StepFactory = newSelfImproveValidateFactory()
var _ plugin.StepFactory = newSelfImproveDiffFactory()

// Compile-time guard: the step types implement the Execute signature that
// legacyStepInstance wraps (typed_contracts.go:169).
var _ interface {
	Execute(context.Context, *module.PipelineContext) (*module.StepResult, error)
} = (*LSPDiagnoseStep)(nil)
var _ interface {
	Execute(context.Context, *module.PipelineContext) (*module.StepResult, error)
} = (*SelfImproveValidateStep)(nil)
var _ interface {
	Execute(context.Context, *module.PipelineContext) (*module.StepResult, error)
} = (*SelfImproveDiffStep)(nil)

// silence unused-import for modular in case the compile-time guards above are
// the only references (they aren't, but keeps the file robust to edits).
var _ modular.Application
