package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GoCodeAlone/workflow-plugin-agent/internal/contracts"
	"github.com/GoCodeAlone/workflow/module"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/anypb"
)

const agentContractsPackage = "workflow.plugins.agent.v1."

// ContractRegistry returns protobuf descriptors and strict contract bindings.
func (p *AgentPlugin) ContractRegistry() *pb.ContractRegistry {
	return &pb.ContractRegistry{
		FileDescriptorSet: &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(contracts.File_internal_contracts_agent_proto),
		}},
		Contracts: []*pb.ContractDescriptor{
			{
				Kind:          pb.ContractKind_CONTRACT_KIND_MODULE,
				ModuleType:    "agent.provider",
				ConfigMessage: agentContractsPackage + "ProviderConfig",
				Mode:          pb.ContractMode_CONTRACT_MODE_STRICT_PROTO,
			},
			stepContract("step.agent_execute", "AgentExecuteConfig", "AgentExecuteInput", "AgentExecuteOutput"),
			stepContract("step.provider_test", "ProviderTestConfig", "ProviderTestInput", "ProviderTestOutput"),
			stepContract("step.provider_models", "ProviderModelsConfig", "ProviderModelsInput", "ProviderModelsOutput"),
			stepContract("step.model_pull", "ModelPullConfig", "ModelPullInput", "ModelPullOutput"),
		},
	}
}

// ModuleTypes implements sdk.ModuleProvider for gRPC-hosted plugin use.
func (p *AgentPlugin) ModuleTypes() []string {
	return []string{"agent.provider"}
}

// CreateModule implements sdk.ModuleProvider with the legacy map boundary.
func (p *AgentPlugin) CreateModule(typeName, name string, config map[string]any) (sdk.ModuleInstance, error) {
	if typeName != "agent.provider" {
		return nil, fmt.Errorf("agent plugin: unknown module type %q", typeName)
	}
	mod := newProviderModuleFactory()(name, config)
	providerMod, ok := mod.(*ProviderModule)
	if !ok {
		return nil, fmt.Errorf("agent plugin: provider factory returned %T", mod)
	}
	return &providerModuleInstance{module: providerMod}, nil
}

// TypedModuleTypes implements sdk.TypedModuleProvider.
func (p *AgentPlugin) TypedModuleTypes() []string {
	return p.ModuleTypes()
}

// CreateTypedModule validates protobuf config and creates the provider module.
func (p *AgentPlugin) CreateTypedModule(typeName, name string, config *anypb.Any) (sdk.ModuleInstance, error) {
	if typeName != "agent.provider" {
		return nil, fmt.Errorf("%w: module type %q", sdk.ErrTypedContractNotHandled, typeName)
	}
	factory := sdk.NewTypedModuleFactory("agent.provider", &contracts.ProviderConfig{}, func(name string, cfg *contracts.ProviderConfig) (sdk.ModuleInstance, error) {
		values, err := protoMessageToMap(cfg)
		if err != nil {
			return nil, err
		}
		return p.CreateModule(typeName, name, values)
	})
	return factory.CreateTypedModule(typeName, name, config)
}

// StepTypes implements sdk.StepProvider for gRPC-hosted plugin use.
func (p *AgentPlugin) StepTypes() []string {
	return []string{"step.agent_execute", "step.provider_test", "step.provider_models", "step.model_pull"}
}

// CreateStep implements sdk.StepProvider with the legacy map boundary.
func (p *AgentPlugin) CreateStep(typeName, name string, config map[string]any) (sdk.StepInstance, error) {
	factory, ok := p.StepFactories()[typeName]
	if !ok {
		return nil, fmt.Errorf("agent plugin: unknown step type %q", typeName)
	}
	step, err := factory(name, config, nil)
	if err != nil {
		return nil, err
	}
	pipelineStep, ok := step.(interface {
		Execute(context.Context, *module.PipelineContext) (*module.StepResult, error)
	})
	if !ok {
		return nil, fmt.Errorf("agent plugin: step factory returned %T", step)
	}
	return legacyStepInstance{step: pipelineStep}, nil
}

// TypedStepTypes implements sdk.TypedStepProvider.
func (p *AgentPlugin) TypedStepTypes() []string {
	return p.StepTypes()
}

// CreateTypedStep validates protobuf config and returns a strict typed adapter.
func (p *AgentPlugin) CreateTypedStep(typeName, name string, config *anypb.Any) (sdk.StepInstance, error) {
	switch typeName {
	case "step.agent_execute":
		factory := sdk.NewTypedStepFactory(typeName, &contracts.AgentExecuteConfig{}, &contracts.AgentExecuteInput{}, typedAgentExecute(name))
		return factory.CreateTypedStep(typeName, name, config)
	case "step.provider_test":
		factory := sdk.NewTypedStepFactory(typeName, &contracts.ProviderTestConfig{}, &contracts.ProviderTestInput{}, typedProviderTest)
		return factory.CreateTypedStep(typeName, name, config)
	case "step.provider_models":
		factory := sdk.NewTypedStepFactory(typeName, &contracts.ProviderModelsConfig{}, &contracts.ProviderModelsInput{}, typedProviderModels)
		return factory.CreateTypedStep(typeName, name, config)
	case "step.model_pull":
		factory := sdk.NewTypedStepFactory(typeName, &contracts.ModelPullConfig{}, &contracts.ModelPullInput{}, typedModelPull(name))
		return factory.CreateTypedStep(typeName, name, config)
	default:
		return nil, fmt.Errorf("%w: step type %q", sdk.ErrTypedContractNotHandled, typeName)
	}
}

func stepContract(stepType, configMessage, inputMessage, outputMessage string) *pb.ContractDescriptor {
	return &pb.ContractDescriptor{
		Kind:          pb.ContractKind_CONTRACT_KIND_STEP,
		StepType:      stepType,
		ConfigMessage: agentContractsPackage + configMessage,
		InputMessage:  agentContractsPackage + inputMessage,
		OutputMessage: agentContractsPackage + outputMessage,
		Mode:          pb.ContractMode_CONTRACT_MODE_STRICT_PROTO,
	}
}

type providerModuleInstance struct {
	module *ProviderModule
}

func (m *providerModuleInstance) Init() error {
	if m.module == nil {
		return fmt.Errorf("provider module is nil")
	}
	if ep, ok := m.module.prov.(*errProvider); ok {
		return ep.err
	}
	return nil
}

func (m *providerModuleInstance) Start(ctx context.Context) error {
	return m.module.Start(ctx)
}

func (m *providerModuleInstance) Stop(ctx context.Context) error {
	return m.module.Stop(ctx)
}

type legacyStepInstance struct {
	step interface {
		Execute(context.Context, *module.PipelineContext) (*module.StepResult, error)
	}
}

func (s legacyStepInstance) Execute(ctx context.Context, triggerData map[string]any, stepOutputs map[string]map[string]any, current map[string]any, metadata map[string]any, _ map[string]any) (*sdk.StepResult, error) {
	result, err := s.step.Execute(ctx, &module.PipelineContext{
		TriggerData: triggerData,
		StepOutputs: stepOutputs,
		Current:     current,
		Metadata:    metadata,
	})
	if err != nil {
		return nil, err
	}
	return &sdk.StepResult{Output: result.Output, StopPipeline: result.Stop}, nil
}

func typedProviderModels(ctx context.Context, req sdk.TypedStepRequest[*contracts.ProviderModelsConfig, *contracts.ProviderModelsInput]) (*sdk.TypedStepResult[*contracts.ProviderModelsOutput], error) {
	current := map[string]any{}
	if req.Input != nil {
		current["type"] = req.Input.GetProviderType()
		current["api_key"] = req.Input.GetApiKey()
		current["base_url"] = req.Input.GetBaseUrl()
	}
	step := &ProviderModelsStep{name: "provider_models"}
	result, err := step.Execute(ctx, &module.PipelineContext{Current: current})
	if err != nil {
		return nil, err
	}
	return &sdk.TypedStepResult[*contracts.ProviderModelsOutput]{Output: providerModelsOutputFromMap(result.Output)}, nil
}

func typedProviderTest(ctx context.Context, req sdk.TypedStepRequest[*contracts.ProviderTestConfig, *contracts.ProviderTestInput]) (*sdk.TypedStepResult[*contracts.ProviderTestOutput], error) {
	alias := ""
	if req.Config != nil {
		alias = req.Config.GetAlias()
	}
	if req.Input != nil && req.Input.GetAlias() != "" {
		alias = req.Input.GetAlias()
	}
	if alias == "" {
		return nil, fmt.Errorf("provider_test step %q: alias is required", "provider_test")
	}
	return &sdk.TypedStepResult[*contracts.ProviderTestOutput]{
		Output: &contracts.ProviderTestOutput{
			Success:   false,
			Message:   "provider registry not available",
			LatencyMs: 0,
		},
	}, nil
}

func typedAgentExecute(name string) sdk.TypedStepHandler[*contracts.AgentExecuteConfig, *contracts.AgentExecuteInput, *contracts.AgentExecuteOutput] {
	return func(context.Context, sdk.TypedStepRequest[*contracts.AgentExecuteConfig, *contracts.AgentExecuteInput]) (*sdk.TypedStepResult[*contracts.AgentExecuteOutput], error) {
		return nil, fmt.Errorf("agent_execute step %q: no application context", name)
	}
}

func typedModelPull(name string) sdk.TypedStepHandler[*contracts.ModelPullConfig, *contracts.ModelPullInput, *contracts.ModelPullOutput] {
	return func(ctx context.Context, req sdk.TypedStepRequest[*contracts.ModelPullConfig, *contracts.ModelPullInput]) (*sdk.TypedStepResult[*contracts.ModelPullOutput], error) {
		cfg, err := protoMessageToMap(req.Config)
		if err != nil {
			return nil, err
		}
		stepAny, err := newModelPullStepFactory()(name, cfg, nil)
		if err != nil {
			return nil, err
		}
		step := stepAny.(*ModelPullStep)
		result, err := step.Execute(ctx, &module.PipelineContext{})
		if err != nil {
			return nil, err
		}
		return &sdk.TypedStepResult[*contracts.ModelPullOutput]{Output: modelPullOutputFromMap(result.Output)}, nil
	}
}

func protoMessageToMap(msg proto.Message) (map[string]any, error) {
	if msg == nil {
		return map[string]any{}, nil
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal typed protobuf config: %w", err)
	}
	values := map[string]any{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("decode typed protobuf config: %w", err)
	}
	return values, nil
}

func providerModelsOutputFromMap(values map[string]any) *contracts.ProviderModelsOutput {
	out := &contracts.ProviderModelsOutput{
		Success: boolValue(values["success"]),
		Error:   stringValue(values["error"]),
	}
	for _, raw := range anySlice(values["models"]) {
		modelMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out.Models = append(out.Models, &contracts.ProviderModel{
			Id:            stringValue(modelMap["id"]),
			Name:          stringValue(modelMap["name"]),
			ContextWindow: int32Value(modelMap["context_window"]),
		})
	}
	return out
}

func modelPullOutputFromMap(values map[string]any) *contracts.ModelPullOutput {
	return &contracts.ModelPullOutput{
		Status:    stringValue(values["status"]),
		ModelPath: stringValue(values["model_path"]),
		SizeBytes: int64Value(values["size_bytes"]),
		Error:     stringValue(values["error"]),
	}
}

func anySlice(v any) []any {
	switch list := v.(type) {
	case []any:
		return list
	default:
		return nil
	}
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func int32Value(v any) int32 {
	return int32(int64Value(v))
}

func int64Value(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}
