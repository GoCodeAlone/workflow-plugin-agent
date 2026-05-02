package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/internal/contracts"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
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

func TestAppBoundStepsAreNotAdvertisedAsStrict(t *testing.T) {
	registry := New().ContractRegistry()
	for _, descriptor := range registry.Contracts {
		switch descriptor.StepType {
		case "step.agent_execute", "step.provider_test":
			t.Fatalf("%s requires application services and must not be advertised as strict", descriptor.StepType)
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
		if _, err := typedProvider.CreateTypedStep(stepType, "app-bound", config); err == nil {
			t.Fatalf("CreateTypedStep(%q) succeeded; app-bound step must use legacy execution", stepType)
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
	for _, stepType := range manifest.Capabilities.StepTypes {
		switch stepType {
		case "step.agent_execute", "step.provider_test":
			t.Fatalf("plugin.json advertises legacy-only step %q while strict contracts are enabled", stepType)
		}
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
