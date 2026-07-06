package provider

import (
	"context"
	"errors"
	"testing"

	awsbedrock "github.com/aws/aws-sdk-go-v2/service/bedrock"
	"github.com/aws/aws-sdk-go-v2/service/bedrock/types"
)

type fakeBedrockModelLister struct {
	input *awsbedrock.ListFoundationModelsInput
	out   *awsbedrock.ListFoundationModelsOutput
	err   error
}

func (f *fakeBedrockModelLister) ListFoundationModels(ctx context.Context, in *awsbedrock.ListFoundationModelsInput, optFns ...func(*awsbedrock.Options)) (*awsbedrock.ListFoundationModelsOutput, error) {
	f.input = in
	return f.out, f.err
}

func TestListBedrockModelsFromAPIListsAllTextModels(t *testing.T) {
	streaming := true
	api := &fakeBedrockModelLister{out: &awsbedrock.ListFoundationModelsOutput{
		ModelSummaries: []types.FoundationModelSummary{
			{
				ModelId:                    strPtr("anthropic.claude-sonnet-4-20250514-v1:0"),
				ModelName:                  strPtr("Claude Sonnet 4"),
				ProviderName:               strPtr("Anthropic"),
				OutputModalities:           []types.ModelModality{types.ModelModalityText},
				InferenceTypesSupported:    []types.InferenceType{types.InferenceTypeOnDemand},
				ResponseStreamingSupported: &streaming,
			},
			{
				ModelId:                    strPtr("amazon.titan-text-lite-v1"),
				ModelName:                  strPtr("Titan Text Lite"),
				ProviderName:               strPtr("Amazon"),
				OutputModalities:           []types.ModelModality{types.ModelModalityText},
				InferenceTypesSupported:    []types.InferenceType{types.InferenceTypeOnDemand},
				ResponseStreamingSupported: &streaming,
			},
			{
				ModelId:                 strPtr("anthropic.claude-image-only"),
				ModelName:               strPtr("Claude Image Only"),
				ProviderName:            strPtr("Anthropic"),
				OutputModalities:        []types.ModelModality{types.ModelModalityImage},
				InferenceTypesSupported: []types.InferenceType{types.InferenceTypeOnDemand},
			},
		},
	}}

	models, err := listBedrockModelsFromAPI(context.Background(), api)
	if err != nil {
		t.Fatalf("listBedrockModelsFromAPI: %v", err)
	}
	if api.input == nil {
		t.Fatal("ListFoundationModels was not called")
	}
	if api.input.ByProvider != nil {
		t.Fatalf("ByProvider = %q, want nil", *api.input.ByProvider)
	}
	if api.input.ByOutputModality != types.ModelModalityText {
		t.Fatalf("ByOutputModality = %q", api.input.ByOutputModality)
	}
	if api.input.ByInferenceType != types.InferenceTypeOnDemand {
		t.Fatalf("ByInferenceType = %q", api.input.ByInferenceType)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ID != "amazon.titan-text-lite-v1" {
		t.Fatalf("first model ID = %q", models[0].ID)
	}
	if models[0].Name != "Amazon Titan Text Lite" {
		t.Fatalf("first model name = %q", models[0].Name)
	}
	if models[1].ID != "anthropic.claude-sonnet-4-20250514-v1:0" {
		t.Fatalf("second model ID = %q", models[1].ID)
	}
	if models[1].Name != "Anthropic Claude Sonnet 4" {
		t.Fatalf("second model name = %q", models[1].Name)
	}
}

func TestListBedrockModelsFromAPIReturnsErrors(t *testing.T) {
	api := &fakeBedrockModelLister{err: errors.New("access denied")}
	_, err := listBedrockModelsFromAPI(context.Background(), api)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListModelsWithSettingsBedrockRequiresCredentials(t *testing.T) {
	_, err := ListModelsWithSettings(context.Background(), "anthropic_bedrock", "", "", nil)
	if err == nil {
		t.Fatal("expected missing credentials error")
	}
}

func TestBedrockModelListConfigFromJSONCredentials(t *testing.T) {
	cfg, err := bedrockModelListConfigFromRequest(ModelListRequest{
		APIKey: `{"region":"us-west-2","access_key_id":"AKIAEXAMPLE","secret_access_key":"secret","session_token":"token"}`,
	})
	if err != nil {
		t.Fatalf("bedrockModelListConfigFromRequest: %v", err)
	}
	if cfg.Region != "us-west-2" {
		t.Fatalf("Region = %q", cfg.Region)
	}
	if cfg.AccessKeyID != "AKIAEXAMPLE" {
		t.Fatalf("AccessKeyID = %q", cfg.AccessKeyID)
	}
	if cfg.SecretAccessKey != "secret" {
		t.Fatalf("SecretAccessKey = %q", cfg.SecretAccessKey)
	}
	if cfg.SessionToken != "token" {
		t.Fatalf("SessionToken = %q", cfg.SessionToken)
	}
}

func strPtr(s string) *string { return &s }
