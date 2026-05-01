package agent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/GoCodeAlone/workflow/schema"
)

// TestAgentPluginStepSchemas verifies that StepSchemas() returns a non-nil
// descriptor for every step type advertised in StepFactories().
func TestAgentPluginStepSchemas(t *testing.T) {
	p := New()

	// Collect advertised step types.
	advertised := make(map[string]bool)
	for k := range p.StepFactories() {
		advertised[k] = true
	}

	// Collect strict-contract descriptors.
	schemas := p.StepSchemas()
	if len(schemas) == 0 {
		t.Fatal("StepSchemas() returned an empty list; expected a descriptor for each step type")
	}

	covered := make(map[string]bool)
	for _, s := range schemas {
		if s.Type == "" {
			t.Error("StepSchema with empty Type field")
			continue
		}
		if s.Description == "" {
			t.Errorf("StepSchema %q: Description must not be empty", s.Type)
		}
		if s.Plugin != "workflow-plugin-agent" {
			t.Errorf("StepSchema %q: Plugin = %q, want %q", s.Type, s.Plugin, "workflow-plugin-agent")
		}
		covered[s.Type] = true
	}

	// Every advertised step must have a descriptor.
	for stepType := range advertised {
		if !covered[stepType] {
			t.Errorf("step type %q is registered in StepFactories() but has no StepSchema contract descriptor", stepType)
		}
	}
}

// TestAgentPluginModuleSchemas verifies that ModuleSchemas() returns a non-nil
// descriptor for every module type advertised in ModuleFactories().
func TestAgentPluginModuleSchemas(t *testing.T) {
	p := New()

	advertised := make(map[string]bool)
	for k := range p.ModuleFactories() {
		advertised[k] = true
	}

	schemas := p.ModuleSchemas()
	if len(schemas) == 0 {
		t.Fatal("ModuleSchemas() returned an empty list; expected a descriptor for each module type")
	}

	covered := make(map[string]bool)
	for _, s := range schemas {
		if s.Type == "" {
			t.Error("ModuleSchema with empty Type field")
			continue
		}
		if s.Description == "" {
			t.Errorf("ModuleSchema %q: Description must not be empty", s.Type)
		}
		covered[s.Type] = true
	}

	for modType := range advertised {
		if !covered[modType] {
			t.Errorf("module type %q is registered in ModuleFactories() but has no ModuleSchema contract descriptor", modType)
		}
	}
}

// TestAgentPluginManifestStepSchemas verifies that the Manifest embedded in the
// plugin has StepSchemas populated for all step types (so external tooling such
// as wfctl plugin docs can enumerate them from the manifest alone).
func TestAgentPluginManifestStepSchemas(t *testing.T) {
	p := New()
	m := p.EngineManifest()
	if m == nil {
		t.Fatal("EngineManifest() returned nil")
	}
	if len(m.StepSchemas) == 0 {
		t.Fatal("EngineManifest().StepSchemas is empty; populate it with agentStepSchemas()")
	}
	for _, s := range m.StepSchemas {
		if s.Type == "" {
			t.Error("manifest StepSchema has empty Type")
		}
	}
}

// TestPluginJSONCanonicalFormat verifies that plugin.json is in canonical format
// (top-level moduleTypes/stepTypes rather than nested inside a capabilities
// object) and that every step type has a matching stepSchemas entry.
func TestPluginJSONCanonicalFormat(t *testing.T) {
	data, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}

	m, err := plugin.LoadManifest("plugin.json")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	// Basic required fields.
	if m.Name == "" {
		t.Error("manifest.Name is empty")
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if m.Author == "" {
		t.Error("manifest.Author is empty")
	}
	if m.Description == "" {
		t.Error("manifest.Description is empty")
	}

	// Verify the canonical (non-legacy) shape: moduleTypes and stepTypes must
	// appear at the top level, not only inside a capabilities object.
	var rawManifest map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawManifest); err != nil {
		t.Fatalf("json.Unmarshal plugin.json: %v", err)
	}
	if _, ok := rawManifest["moduleTypes"]; !ok {
		t.Error("plugin.json: moduleTypes missing at top level (canonical format required)")
	}
	if _, ok := rawManifest["stepTypes"]; !ok {
		t.Error("plugin.json: stepTypes missing at top level (canonical format required)")
	}
	if _, ok := rawManifest["stepSchemas"]; !ok {
		t.Error("plugin.json: stepSchemas missing (required for strict contract descriptors)")
	}

	// Every step type listed in stepTypes must have a stepSchemas entry.
	stepSchemaIndex := make(map[string]*schema.StepSchema)
	for _, s := range m.StepSchemas {
		if s != nil {
			stepSchemaIndex[s.Type] = s
		}
	}
	for _, stepType := range m.StepTypes {
		if _, ok := stepSchemaIndex[stepType]; !ok {
			t.Errorf("step type %q listed in stepTypes but missing from stepSchemas", stepType)
		}
	}
}
