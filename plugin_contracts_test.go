package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/internal/contracts"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestContractRegistryDeclaresStrictContracts(t *testing.T) {
	provider := New()
	contractProvider, ok := any(provider).(sdk.ContractProvider)
	if !ok {
		t.Fatal("expected AgentPlugin to expose sdk.ContractProvider")
	}

	registry := contractProvider.ContractRegistry()
	if registry == nil {
		t.Fatal("expected contract registry")
	}
	if registry.FileDescriptorSet == nil || len(registry.FileDescriptorSet.File) == 0 {
		t.Fatal("expected file descriptor set for plugin-owned messages")
	}

	want := map[string]contractExpectation{
		"module:agent.provider": {
			kind:   pb.ContractKind_CONTRACT_KIND_MODULE,
			config: "workflow.plugins.agent.v1.ProviderConfig",
		},
		"step:step.agent_execute": {
			kind:   pb.ContractKind_CONTRACT_KIND_STEP,
			config: "workflow.plugins.agent.v1.AgentExecuteConfig",
			input:  "workflow.plugins.agent.v1.AgentExecuteInput",
			output: "workflow.plugins.agent.v1.AgentExecuteOutput",
		},
		"step:step.provider_test": {
			kind:   pb.ContractKind_CONTRACT_KIND_STEP,
			config: "workflow.plugins.agent.v1.ProviderTestConfig",
			input:  "workflow.plugins.agent.v1.ProviderTestInput",
			output: "workflow.plugins.agent.v1.ProviderTestOutput",
		},
		"step:step.provider_models": {
			kind:   pb.ContractKind_CONTRACT_KIND_STEP,
			config: "workflow.plugins.agent.v1.ProviderModelsConfig",
			input:  "workflow.plugins.agent.v1.ProviderModelsInput",
			output: "workflow.plugins.agent.v1.ProviderModelsOutput",
		},
		"step:step.model_pull": {
			kind:   pb.ContractKind_CONTRACT_KIND_STEP,
			config: "workflow.plugins.agent.v1.ModelPullConfig",
			input:  "workflow.plugins.agent.v1.ModelPullInput",
			output: "workflow.plugins.agent.v1.ModelPullOutput",
		},
		// The 3 stateless orchestrator steps carry strict contract DESCRIPTORS
		// in their own proto package (workflow.plugins.orchestrator.v1). Like
		// the app-bound agent pair above, the descriptors describe the contract
		// SURFACE; the gRPC serving surface is wired in PR4.
		"step:step.lsp_diagnose": {
			kind:   pb.ContractKind_CONTRACT_KIND_STEP,
			config: "workflow.plugins.orchestrator.v1.LspDiagnoseConfig",
			input:  "workflow.plugins.orchestrator.v1.LspDiagnoseInput",
			output: "workflow.plugins.orchestrator.v1.LspDiagnoseOutput",
		},
		"step:step.self_improve_validate": {
			kind:   pb.ContractKind_CONTRACT_KIND_STEP,
			config: "workflow.plugins.orchestrator.v1.SelfImproveValidateConfig",
			input:  "workflow.plugins.orchestrator.v1.SelfImproveValidateInput",
			output: "workflow.plugins.orchestrator.v1.SelfImproveValidateOutput",
		},
		"step:step.self_improve_diff": {
			kind:   pb.ContractKind_CONTRACT_KIND_STEP,
			config: "workflow.plugins.orchestrator.v1.SelfImproveDiffConfig",
			input:  "workflow.plugins.orchestrator.v1.SelfImproveDiffInput",
			output: "workflow.plugins.orchestrator.v1.SelfImproveDiffOutput",
		},
	}

	for _, descriptor := range registry.Contracts {
		key := descriptorKey(descriptor)
		expect, ok := want[key]
		if !ok {
			t.Fatalf("unexpected contract %q", key)
		}
		if descriptor.Mode != pb.ContractMode_CONTRACT_MODE_STRICT_PROTO {
			t.Fatalf("%s mode = %s, want strict proto", key, descriptor.Mode)
		}
		if descriptor.Kind != expect.kind {
			t.Fatalf("%s kind = %s, want %s", key, descriptor.Kind, expect.kind)
		}
		if descriptor.ConfigMessage != expect.config {
			t.Fatalf("%s config = %q, want %q", key, descriptor.ConfigMessage, expect.config)
		}
		if descriptor.InputMessage != expect.input {
			t.Fatalf("%s input = %q, want %q", key, descriptor.InputMessage, expect.input)
		}
		if descriptor.OutputMessage != expect.output {
			t.Fatalf("%s output = %q, want %q", key, descriptor.OutputMessage, expect.output)
		}
		delete(want, key)
	}
	if len(want) > 0 {
		t.Fatalf("missing contracts: %#v", want)
	}
}

// TestAppBoundStepsAdvertisedStrictButLegacyExecuted documents the hybrid
// contract for step.agent_execute + step.provider_test: they carry strict
// contract DESCRIPTORS (so consumers know their config/input/output shape and
// the strict-contracts coverage gate passes) but remain on the legacy map
// execution path because their implementations lazy-lookup services from
// modular.Application, which the typed gRPC handler signature cannot access.
// CreateTypedStep therefore declines them (ErrTypedContractNotHandled) and the
// engine falls back to the legacy StepProvider path.
func TestAppBoundStepsAdvertisedStrictButLegacyExecuted(t *testing.T) {
	registry := New().ContractRegistry()
	descriptors := map[string]*pb.ContractDescriptor{}
	for _, descriptor := range registry.Contracts {
		if descriptor.Kind == pb.ContractKind_CONTRACT_KIND_STEP {
			descriptors[descriptor.StepType] = descriptor
		}
	}

	for _, stepType := range []string{"step.agent_execute", "step.provider_test"} {
		desc, ok := descriptors[stepType]
		if !ok {
			t.Fatalf("%s missing strict contract descriptor", stepType)
		}
		if desc.Mode != pb.ContractMode_CONTRACT_MODE_STRICT_PROTO {
			t.Fatalf("%s mode = %s, want strict proto", stepType, desc.Mode)
		}
		if desc.ConfigMessage == "" || desc.InputMessage == "" || desc.OutputMessage == "" {
			t.Fatalf("%s descriptor must carry config/input/output messages", stepType)
		}
	}

	typedProvider, ok := any(New()).(sdk.TypedStepProvider)
	if !ok {
		t.Fatal("expected AgentPlugin to expose sdk.TypedStepProvider")
	}
	for _, stepType := range []string{"step.agent_execute", "step.provider_test"} {
		config, err := anypb.New(&contracts.ProviderModelsConfig{})
		if err != nil {
			t.Fatalf("pack config: %v", err)
		}
		_, err = typedProvider.CreateTypedStep(stepType, "app-bound", config)
		if err == nil {
			t.Fatalf("CreateTypedStep(%q) succeeded; app-bound step must decline typed execution so the engine uses the legacy path", stepType)
		}
		if !errors.Is(err, sdk.ErrTypedContractNotHandled) {
			t.Fatalf("CreateTypedStep(%q) error = %v, want ErrTypedContractNotHandled", stepType, err)
		}
	}

	// TypedStepTypes advertises only the two fully-typed steps; the app-bound
	// pair is intentionally absent because they cannot execute via the typed
	// handler signature. StepTypes() (legacy) still lists all four.
	typedStepTypes := typedProvider.TypedStepTypes()
	for _, stepType := range []string{"step.agent_execute", "step.provider_test"} {
		for _, got := range typedStepTypes {
			if got == stepType {
				t.Fatalf("TypedStepTypes must not list app-bound %s (legacy-execution only)", stepType)
			}
		}
	}
}

func TestPluginContractsManifestMatchesRegistry(t *testing.T) {
	registry := New().ContractRegistry()
	manifest := loadContractManifest(t)

	if manifest.Version != "v1" {
		t.Fatalf("manifest version = %q, want v1", manifest.Version)
	}
	if len(manifest.Contracts) != len(registry.Contracts) {
		t.Fatalf("manifest contracts = %d, registry contracts = %d", len(manifest.Contracts), len(registry.Contracts))
	}

	manifestContracts := make(map[string]manifestContract)
	for _, contract := range manifest.Contracts {
		manifestContracts[contract.Kind+":"+contract.Type] = contract
	}
	for _, descriptor := range registry.Contracts {
		key := descriptorKey(descriptor)
		contract, ok := manifestContracts[key]
		if !ok {
			t.Fatalf("%s missing from plugin.contracts.json", key)
		}
		if contract.Mode != "strict" {
			t.Fatalf("%s mode = %q, want strict", key, contract.Mode)
		}
		if contract.ConfigMessage != descriptor.ConfigMessage {
			t.Fatalf("%s config = %q, want %q", key, contract.ConfigMessage, descriptor.ConfigMessage)
		}
		if contract.InputMessage != descriptor.InputMessage {
			t.Fatalf("%s input = %q, want %q", key, contract.InputMessage, descriptor.InputMessage)
		}
		if contract.OutputMessage != descriptor.OutputMessage {
			t.Fatalf("%s output = %q, want %q", key, contract.OutputMessage, descriptor.OutputMessage)
		}
	}
}

// TestOrchestratorProtoMessagesResolveInRegistry is the strict-contracts
// load-bearing test for the 3 stateless orchestrator steps. Strict-contracts
// config validation resolves each descriptor's config/input/output message by
// fully-qualified name against the proto registry; if the orchestrator.v1
// messages aren't registered (e.g. the generated orchestrator.pb.go is missing
// or its init() never runs), strict-contracts validation rejects pipelines
// using step.lsp_diagnose / step.self_improve_validate / step.self_improve_diff.
// This test proves the messages resolve both via the global proto registry AND
// via the plugin's advertised FileDescriptorSet.
func TestOrchestratorProtoMessagesResolveInRegistry(t *testing.T) {
	// 1. Global proto registry: each message must resolve by full name. This is
	//    what strict-contracts' message resolver consults at validation time.
	wantMessages := []string{
		"workflow.plugins.orchestrator.v1.LspDiagnoseConfig",
		"workflow.plugins.orchestrator.v1.LspDiagnoseInput",
		"workflow.plugins.orchestrator.v1.LspDiagnoseOutput",
		"workflow.plugins.orchestrator.v1.SelfImproveValidateConfig",
		"workflow.plugins.orchestrator.v1.SelfImproveValidateInput",
		"workflow.plugins.orchestrator.v1.SelfImproveValidateOutput",
		"workflow.plugins.orchestrator.v1.SelfImproveDiffConfig",
		"workflow.plugins.orchestrator.v1.SelfImproveDiffInput",
		"workflow.plugins.orchestrator.v1.SelfImproveDiffOutput",
	}
	for _, fullName := range wantMessages {
		desc, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(fullName))
		if err != nil {
			t.Fatalf("proto registry cannot resolve %q: %v (orchestrator.pb.go missing or not imported — strict-contracts will reject these step types)", fullName, err)
		}
		if desc == nil {
			t.Fatalf("proto registry returned nil descriptor for %q", fullName)
		}
	}

	// 2. The plugin's ContractRegistry FileDescriptorSet must include the
	//    orchestrator.proto file so consumers can build a self-contained
	//    descriptor set without relying on the global registry.
	registry := New().ContractRegistry()
	if registry.FileDescriptorSet == nil {
		t.Fatal("ContractRegistry FileDescriptorSet is nil")
	}
	foundOrchestratorProto := false
	for _, f := range registry.FileDescriptorSet.File {
		if f.GetName() == "internal/contracts/orchestrator.proto" {
			foundOrchestratorProto = true
			break
		}
	}
	if !foundOrchestratorProto {
		t.Fatal("ContractRegistry FileDescriptorSet missing internal/contracts/orchestrator.proto (consumers cannot resolve orchestrator.v1 messages from the advertised descriptor set)")
	}

	// 3. Round-trip: instantiate each Config message (forces the typed Go
	//    struct to exist + be wired to its descriptor) and confirm its
	//    descriptor FullName matches the descriptor references.
	samples := []struct {
		msg    protoreflect.ProtoMessage
		fullName string
	}{
		{&contracts.LspDiagnoseConfig{}, "workflow.plugins.orchestrator.v1.LspDiagnoseConfig"},
		{&contracts.SelfImproveValidateConfig{}, "workflow.plugins.orchestrator.v1.SelfImproveValidateConfig"},
		{&contracts.SelfImproveDiffConfig{}, "workflow.plugins.orchestrator.v1.SelfImproveDiffConfig"},
	}
	for _, s := range samples {
		got := string(s.msg.ProtoReflect().Descriptor().FullName())
		if got != s.fullName {
			t.Fatalf("%T descriptor FullName = %q, want %q", s.msg, got, s.fullName)
		}
	}
}

// TestOrchestratorStepDescriptorsConsistentAcrossSources is the three-source
// consistency gate for the orchestrator contract triad: the SAME 3 step types
// must appear (with matching config/input/output message names) in
//   - the runtime ContractRegistry (Go),
//   - plugin.contracts.json (declarative), and
//   - plugin.json capabilities.stepTypes (advertisement).
// This catches the drift class where a descriptor is added to one source but
// not the others (e.g. a step advertised in plugin.json with no contract
// descriptor, or a descriptor whose message reference has no generated proto).
func TestOrchestratorStepDescriptorsConsistentAcrossSources(t *testing.T) {
	orchestratorSteps := []struct {
		stepType string
		config   string
		input    string
		output   string
	}{
		{"step.lsp_diagnose", "LspDiagnoseConfig", "LspDiagnoseInput", "LspDiagnoseOutput"},
		{"step.self_improve_validate", "SelfImproveValidateConfig", "SelfImproveValidateInput", "SelfImproveValidateOutput"},
		{"step.self_improve_diff", "SelfImproveDiffConfig", "SelfImproveDiffInput", "SelfImproveDiffOutput"},
	}
	const pkg = "workflow.plugins.orchestrator.v1."

	// Source 1: runtime registry.
	registry := New().ContractRegistry()
	runtimeByStep := map[string]*pb.ContractDescriptor{}
	for _, d := range registry.Contracts {
		if d.Kind == pb.ContractKind_CONTRACT_KIND_STEP {
			runtimeByStep[d.StepType] = d
		}
	}
	// Source 2: plugin.contracts.json.
	manifest := loadContractManifest(t)
	manifestByStep := map[string]manifestContract{}
	for _, c := range manifest.Contracts {
		manifestByStep[c.Kind+":"+c.Type] = c
	}
	// Source 3: plugin.json capabilities.stepTypes.
	pluginJSON := loadPluginJSONCapabilitiesForContracts(t)
	declaredSteps := map[string]bool{}
	for _, s := range pluginJSON.Capabilities.StepTypes {
		declaredSteps[s] = true
	}

	for _, want := range orchestratorSteps {
		// Runtime.
		desc, ok := runtimeByStep[want.stepType]
		if !ok {
			t.Fatalf("%s missing from runtime ContractRegistry", want.stepType)
		}
		if desc.Mode != pb.ContractMode_CONTRACT_MODE_STRICT_PROTO {
			t.Fatalf("%s runtime mode = %s, want strict proto", want.stepType, desc.Mode)
		}
		if got := desc.ConfigMessage; got != pkg+want.config {
			t.Fatalf("%s runtime config = %q, want %q", want.stepType, got, pkg+want.config)
		}
		if got := desc.InputMessage; got != pkg+want.input {
			t.Fatalf("%s runtime input = %q, want %q", want.stepType, got, pkg+want.input)
		}
		if got := desc.OutputMessage; got != pkg+want.output {
			t.Fatalf("%s runtime output = %q, want %q", want.stepType, got, pkg+want.output)
		}

		// Manifest (plugin.contracts.json).
		mc, ok := manifestByStep["step:"+want.stepType]
		if !ok {
			t.Fatalf("%s missing from plugin.contracts.json", want.stepType)
		}
		if mc.Mode != "strict" {
			t.Fatalf("%s manifest mode = %q, want strict", want.stepType, mc.Mode)
		}
		if mc.ConfigMessage != pkg+want.config {
			t.Fatalf("%s manifest config = %q, want %q", want.stepType, mc.ConfigMessage, pkg+want.config)
		}
		if mc.InputMessage != pkg+want.input {
			t.Fatalf("%s manifest input = %q, want %q", want.stepType, mc.InputMessage, pkg+want.input)
		}
		if mc.OutputMessage != pkg+want.output {
			t.Fatalf("%s manifest output = %q, want %q", want.stepType, mc.OutputMessage, pkg+want.output)
		}

		// Advertisement (plugin.json).
		if !declaredSteps[want.stepType] {
			t.Fatalf("%s declared in runtime registry + plugin.contracts.json but NOT advertised in plugin.json capabilities.stepTypes", want.stepType)
		}
	}

	// plugin.json must declare exactly the 7-type union (4 agent + 3
	// orchestrator) — the Phase 2b contracts-first advertisement.
	if want, got := 7, len(pluginJSON.Capabilities.StepTypes); got != want {
		t.Fatalf("plugin.json stepTypes len = %d, want %d (4 agent + 3 orchestrator)", got, want)
	}
}

// loadPluginJSONCapabilitiesForContracts reads plugin.json capabilities for the
// three-source consistency test (defined here to keep plugin_contracts_test.go
// self-contained; provider_manifest_test.go has its own loader for its drift
// gates).
func loadPluginJSONCapabilitiesForContracts(t *testing.T) struct {
	Capabilities struct {
		StepTypes []string `json:"stepTypes"`
	} `json:"capabilities"`
} {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var out struct {
		Capabilities struct {
			StepTypes []string `json:"stepTypes"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	return out
}

func TestPluginJSONDeclaresStrictContractMetadata(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var manifest struct {
		Capabilities struct {
			StepTypes []string `json:"stepTypes"`
		} `json:"capabilities"`
		StrictContracts struct {
			Enabled  bool   `json:"enabled"`
			Mode     string `json:"mode"`
			Registry string `json:"registry"`
		} `json:"strictContracts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	if !manifest.StrictContracts.Enabled {
		t.Fatal("strictContracts.enabled = false, want true")
	}
	if manifest.StrictContracts.Mode != "strict" {
		t.Fatalf("strictContracts.mode = %q, want strict", manifest.StrictContracts.Mode)
	}
	if manifest.StrictContracts.Registry != "plugin.contracts.json" {
		t.Fatalf("strictContracts.registry = %q, want plugin.contracts.json", manifest.StrictContracts.Registry)
	}
	// plugin.json capabilities.stepTypes lists ALL runtime steps. All four
	// carry strict contract descriptors in plugin.contracts.json (asserted by
	// TestPluginContractsManifestMatchesRegistry); step.agent_execute and
	// step.provider_test remain legacy-execution despite having descriptors
	// (asserted by TestAppBoundStepsAdvertisedStrictButLegacyExecuted).
	// The drift gate that plugin.json stepTypes == runtime StepTypes() lives
	// in provider_manifest_test.go (TestPluginJSONStepTypesMatchRuntimeTruth).
	if len(manifest.Capabilities.StepTypes) == 0 {
		t.Fatal("plugin.json capabilities.stepTypes is empty; expected runtime-truth advertisement")
	}
}

func TestTypedProviderModelsPreservesLegacyMissingTypeOutput(t *testing.T) {
	result, err := typedProviderModels(context.Background(), sdk.TypedStepRequest[*contracts.ProviderModelsConfig, *contracts.ProviderModelsInput]{
		Config: &contracts.ProviderModelsConfig{},
		Input:  &contracts.ProviderModelsInput{},
	})
	if err != nil {
		t.Fatalf("typedProviderModels: %v", err)
	}
	if result == nil || result.Output == nil {
		t.Fatal("expected typed output")
	}
	if result.Output.GetSuccess() {
		t.Fatal("expected missing provider type to fail")
	}
	if got := result.Output.GetError(); got != "provider type is required" {
		t.Fatalf("error = %q, want provider type is required", got)
	}
	if len(result.Output.GetModels()) != 0 {
		t.Fatalf("models = %d, want empty", len(result.Output.GetModels()))
	}
}

func TestAdvertisedStrictStepsTypedExecution(t *testing.T) {
	tempDir := t.TempDir()
	modelPath := filepath.Join(tempDir, "org--model", "weights.bin")
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		t.Fatalf("mkdir model cache: %v", err)
	}
	if err := os.WriteFile(modelPath, []byte("cached model"), 0o644); err != nil {
		t.Fatalf("write cached model: %v", err)
	}

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "step.provider_models",
			run: func(t *testing.T) {
				result, err := typedProviderModels(context.Background(), sdk.TypedStepRequest[*contracts.ProviderModelsConfig, *contracts.ProviderModelsInput]{
					Config: &contracts.ProviderModelsConfig{},
					Input:  &contracts.ProviderModelsInput{},
				})
				if err != nil {
					t.Fatalf("typedProviderModels: %v", err)
				}
				if result == nil || result.Output == nil {
					t.Fatal("expected typed output")
				}
				if result.Output.GetSuccess() {
					t.Fatal("expected legacy-equivalent missing provider type response")
				}
				if got := result.Output.GetError(); got != "provider type is required" {
					t.Fatalf("error = %q, want provider type is required", got)
				}
			},
		},
		{
			name: "step.model_pull",
			run: func(t *testing.T) {
				result, err := typedModelPull("pull")(context.Background(), sdk.TypedStepRequest[*contracts.ModelPullConfig, *contracts.ModelPullInput]{
					Config: &contracts.ModelPullConfig{
						Source:    "huggingface",
						Model:     "org/model",
						File:      "weights.bin",
						OutputDir: tempDir,
					},
					Input: &contracts.ModelPullInput{},
				})
				if err != nil {
					t.Fatalf("typedModelPull: %v", err)
				}
				if result == nil || result.Output == nil {
					t.Fatal("expected typed output")
				}
				if got := result.Output.GetStatus(); got != "downloaded" {
					t.Fatalf("status = %q, want downloaded", got)
				}
				if got := result.Output.GetModelPath(); got != modelPath {
					t.Fatalf("model_path = %q, want %q", got, modelPath)
				}
				if got := result.Output.GetSizeBytes(); got != int64(len("cached model")) {
					t.Fatalf("size_bytes = %d, want %d", got, len("cached model"))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestCreateTypedStepValidatesAdvertisedStrictStepConfigs(t *testing.T) {
	provider := New()
	typedProvider, ok := any(provider).(sdk.TypedStepProvider)
	if !ok {
		t.Fatal("expected AgentPlugin to expose sdk.TypedStepProvider")
	}
	config, err := anypb.New(&contracts.ProviderModelsConfig{})
	if err != nil {
		t.Fatalf("pack config: %v", err)
	}
	step, err := typedProvider.CreateTypedStep("step.provider_models", "models", config)
	if err != nil {
		t.Fatalf("CreateTypedStep: %v", err)
	}
	if step == nil {
		t.Fatal("expected typed step")
	}

	wrongConfig, err := anypb.New(wrapperspb.String("wrong"))
	if err != nil {
		t.Fatalf("pack wrong config: %v", err)
	}
	for _, stepType := range []string{"step.provider_models", "step.model_pull"} {
		if _, err := typedProvider.CreateTypedStep(stepType, "typed", wrongConfig); err == nil {
			t.Fatalf("CreateTypedStep(%q) accepted wrong config type", stepType)
		} else if !strings.Contains(err.Error(), "typed config type mismatch") {
			t.Fatalf("CreateTypedStep(%q) error = %q, want typed config type mismatch", stepType, err)
		}
	}
}

func TestCreateTypedModuleDecodesScriptedToolCallArgumentsJSON(t *testing.T) {
	typedProvider, ok := any(New()).(sdk.TypedModuleProvider)
	if !ok {
		t.Fatal("expected AgentPlugin to expose sdk.TypedModuleProvider")
	}
	config, err := anypb.New(&contracts.ProviderConfig{
		Provider: "test",
		TestMode: "scripted",
		Steps: []*contracts.ScriptedStep{
			{
				Content: "call tool",
				ToolCalls: []*contracts.ToolCall{
					{
						Id:            "call-1",
						Name:          "echo",
						ArgumentsJson: `{"message":"hello","count":2}`,
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("pack provider config: %v", err)
	}

	instance, err := typedProvider.CreateTypedModule("agent.provider", "typed-test-provider", config)
	if err != nil {
		t.Fatalf("CreateTypedModule: %v", err)
	}
	wrapped, ok := instance.(*sdk.TypedModuleInstance[*contracts.ProviderConfig])
	if !ok {
		t.Fatalf("typed module = %T, want *sdk.TypedModuleInstance", instance)
	}
	providerInstance, ok := wrapped.ModuleInstance.(*providerModuleInstance)
	if !ok {
		t.Fatalf("wrapped module = %T, want *providerModuleInstance", wrapped.ModuleInstance)
	}
	response, err := providerInstance.module.Provider().Chat(context.Background(), []provider.Message{{Role: provider.RoleUser, Content: "run"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(response.ToolCalls))
	}
	args := response.ToolCalls[0].Arguments
	if args["message"] != "hello" {
		t.Fatalf("message arg = %#v, want hello", args["message"])
	}
	if args["count"] != float64(2) {
		t.Fatalf("count arg = %#v, want 2", args["count"])
	}
}

type contractExpectation struct {
	kind   pb.ContractKind
	config string
	input  string
	output string
}

type contractManifest struct {
	Version   string             `json:"version"`
	Contracts []manifestContract `json:"contracts"`
}

type manifestContract struct {
	Kind          string `json:"kind"`
	Type          string `json:"type"`
	Mode          string `json:"mode"`
	ConfigMessage string `json:"config"`
	InputMessage  string `json:"input"`
	OutputMessage string `json:"output"`
}

func loadContractManifest(t *testing.T) contractManifest {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "plugin.contracts.json"))
	if err != nil {
		t.Fatalf("read plugin.contracts.json: %v", err)
	}
	var manifest contractManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse plugin.contracts.json: %v", err)
	}
	return manifest
}

func descriptorKey(descriptor *pb.ContractDescriptor) string {
	switch descriptor.Kind {
	case pb.ContractKind_CONTRACT_KIND_MODULE:
		return "module:" + descriptor.ModuleType
	case pb.ContractKind_CONTRACT_KIND_STEP:
		return "step:" + descriptor.StepType
	default:
		return "unknown"
	}
}
