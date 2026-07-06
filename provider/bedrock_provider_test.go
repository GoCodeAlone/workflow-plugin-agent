package provider

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

type fakeBedrockConverseAPI struct {
	input *bedrockruntime.ConverseInput
	out   *bedrockruntime.ConverseOutput
	err   error
}

func (f *fakeBedrockConverseAPI) Converse(ctx context.Context, in *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	f.input = in
	return f.out, f.err
}

func TestBedrockProviderChatUsesConverseForNonAnthropicModel(t *testing.T) {
	api := &fakeBedrockConverseAPI{out: &bedrockruntime.ConverseOutput{
		Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
			Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: "Hello from Nova"},
			},
		}},
		Usage: &brtypes.TokenUsage{
			InputTokens:  aws.Int32(11),
			OutputTokens: aws.Int32(4),
		},
	}}

	p, err := NewBedrockProvider(BedrockConfig{
		Name:            "bedrock",
		Region:          "us-east-1",
		Model:           "amazon.nova-lite-v1:0",
		MaxTokens:       512,
		AccessKeyID:     "AKIDTEST",
		SecretAccessKey: "secret",
		RuntimeAPI:      api,
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := p.Chat(t.Context(), []Message{
		{Role: RoleSystem, Content: "You are helpful."},
		{Role: RoleUser, Content: "Hi"},
	}, []ToolDef{{
		Name:        "lookup",
		Description: "Look up a value.",
		Parameters:  map[string]any{"type": "object"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	if api.input == nil {
		t.Fatal("Converse was not called")
	}
	if got := aws.ToString(api.input.ModelId); got != "amazon.nova-lite-v1:0" {
		t.Fatalf("ModelId = %q", got)
	}
	if len(api.input.System) != 1 {
		t.Fatalf("System blocks = %d", len(api.input.System))
	}
	if len(api.input.Messages) != 1 || api.input.Messages[0].Role != brtypes.ConversationRoleUser {
		t.Fatalf("Messages = %#v", api.input.Messages)
	}
	if api.input.InferenceConfig == nil || api.input.InferenceConfig.MaxTokens == nil || *api.input.InferenceConfig.MaxTokens != 512 {
		t.Fatalf("InferenceConfig.MaxTokens = %#v", api.input.InferenceConfig)
	}
	if api.input.ToolConfig == nil || len(api.input.ToolConfig.Tools) != 1 {
		t.Fatalf("ToolConfig = %#v", api.input.ToolConfig)
	}
	if resp.Content != "Hello from Nova" {
		t.Fatalf("Content = %q", resp.Content)
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 4 {
		t.Fatalf("Usage = %+v", resp.Usage)
	}
}
