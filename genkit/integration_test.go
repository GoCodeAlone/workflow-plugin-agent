//go:build integration

package genkit_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/genkit"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// envOrSkip returns the value of an environment variable, or skips the test if unset.
func envOrSkip(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("skipping: %s not set", key)
	}
	return v
}

// chatRoundTrip sends a simple message and asserts a non-empty text response.
func chatRoundTrip(t *testing.T, p provider.Provider) {
	t.Helper()
	ctx := context.Background()
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "Say exactly: pong"}}
	resp, err := p.Chat(ctx, msgs, nil)
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content == "" {
		t.Error("expected non-empty content")
	}
}

// streamRoundTrip streams a simple message and asserts at least one text event and a done event.
func streamRoundTrip(t *testing.T, p provider.Provider) {
	t.Helper()
	ctx := context.Background()
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "Say exactly: pong"}}
	ch, err := p.Stream(ctx, msgs, nil)
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	var gotText, gotDone bool
	for ev := range ch {
		switch ev.Type {
		case "text":
			if ev.Text != "" {
				gotText = true
			}
		case "done":
			gotDone = true
		case "error":
			t.Fatalf("stream error event: %s", ev.Error)
		}
	}
	if !gotText {
		t.Error("expected at least one text event")
	}
	if !gotDone {
		t.Error("expected a done event")
	}
}

// TestIntegration_Anthropic tests the Anthropic (direct API) provider.
func TestIntegration_Anthropic(t *testing.T) {
	apiKey := envOrSkip(t, "ANTHROPIC_API_KEY")
	p, err := genkit.NewAnthropicProvider(context.Background(), apiKey, "claude-haiku-4-5-20251001", "", 256)
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	if p.Name() != "anthropic" {
		t.Errorf("expected name 'anthropic', got %q", p.Name())
	}
	chatRoundTrip(t, p)
	streamRoundTrip(t, p)
}

// TestIntegration_OpenAI tests the OpenAI provider.
func TestIntegration_OpenAI(t *testing.T) {
	apiKey := envOrSkip(t, "OPENAI_API_KEY")
	p, err := genkit.NewOpenAIProvider(context.Background(), apiKey, "gpt-4o-mini", "", 256)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	if p.Name() != "openai" {
		t.Errorf("expected name 'openai', got %q", p.Name())
	}
	chatRoundTrip(t, p)
	streamRoundTrip(t, p)
}

// TestIntegration_GoogleAI tests the Google AI (Gemini API) provider.
func TestIntegration_GoogleAI(t *testing.T) {
	apiKey := envOrSkip(t, "GOOGLE_AI_API_KEY")
	p, err := genkit.NewGoogleAIProvider(context.Background(), apiKey, "gemini-2.0-flash", 256)
	if err != nil {
		t.Fatalf("NewGoogleAIProvider: %v", err)
	}
	if p.Name() != "googleai" {
		t.Errorf("expected name 'googleai', got %q", p.Name())
	}
	chatRoundTrip(t, p)
	streamRoundTrip(t, p)
}

// TestIntegration_Ollama tests the Ollama local provider.
func TestIntegration_Ollama(t *testing.T) {
	serverAddr := os.Getenv("OLLAMA_SERVER")
	if serverAddr == "" {
		serverAddr = "http://localhost:11434"
	}
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		t.Skip("skipping: OLLAMA_MODEL not set")
	}
	p, err := genkit.NewOllamaProvider(context.Background(), model, serverAddr, 256)
	if err != nil {
		t.Fatalf("NewOllamaProvider: %v", err)
	}
	if p.Name() != "ollama" {
		t.Errorf("expected name 'ollama', got %q", p.Name())
	}
	chatRoundTrip(t, p)
	streamRoundTrip(t, p)
}

// TestIntegration_OpenRouter tests the OpenRouter provider (OpenAI-compatible).
func TestIntegration_OpenRouter(t *testing.T) {
	apiKey := envOrSkip(t, "OPENROUTER_API_KEY")
	model := os.Getenv("OPENROUTER_MODEL")
	if model == "" {
		model = "openai/gpt-4o-mini"
	}
	p, err := genkit.NewOpenAICompatibleProvider(
		context.Background(), "openrouter", apiKey, model,
		"https://openrouter.ai/api/v1", 256,
	)
	if err != nil {
		t.Fatalf("NewOpenAICompatibleProvider: %v", err)
	}
	chatRoundTrip(t, p)
}

// TestIntegration_Bedrock tests the AWS Bedrock Anthropic provider.
func TestIntegration_Bedrock(t *testing.T) {
	accessKey := envOrSkip(t, "AWS_ACCESS_KEY_ID")
	secretKey := envOrSkip(t, "AWS_SECRET_ACCESS_KEY")
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	p, err := genkit.NewBedrockProvider(
		context.Background(),
		region, "anthropic.claude-haiku-4-20250514-v1:0",
		accessKey, secretKey, "", "", 256,
	)
	if err != nil {
		t.Fatalf("NewBedrockProvider: %v", err)
	}
	chatRoundTrip(t, p)
}

// TestIntegration_VertexAI tests the Google Vertex AI provider.
func TestIntegration_VertexAI(t *testing.T) {
	projectID := envOrSkip(t, "VERTEX_PROJECT_ID")
	region := os.Getenv("VERTEX_REGION")
	if region == "" {
		region = "us-central1"
	}
	p, err := genkit.NewVertexAIProvider(
		context.Background(),
		projectID, region, "gemini-2.0-flash", "", 256,
	)
	if err != nil {
		t.Fatalf("NewVertexAIProvider: %v", err)
	}
	chatRoundTrip(t, p)
}

// TestIntegration_AzureOpenAI tests the Azure OpenAI provider.
func TestIntegration_AzureOpenAI(t *testing.T) {
	resource := envOrSkip(t, "AZURE_OPENAI_RESOURCE")
	deploymentName := envOrSkip(t, "AZURE_OPENAI_DEPLOYMENT")
	apiKey := envOrSkip(t, "AZURE_OPENAI_API_KEY")
	p, err := genkit.NewAzureOpenAIProvider(
		context.Background(),
		resource, deploymentName, "2024-10-21", apiKey, "", 256,
	)
	if err != nil {
		t.Fatalf("NewAzureOpenAIProvider: %v", err)
	}
	chatRoundTrip(t, p)
}

// TestIntegration_AnthropicFoundry tests the Anthropic on Azure AI Foundry provider.
func TestIntegration_AnthropicFoundry(t *testing.T) {
	resource := envOrSkip(t, "FOUNDRY_RESOURCE")
	apiKey := envOrSkip(t, "FOUNDRY_API_KEY")
	model := os.Getenv("FOUNDRY_MODEL")
	if model == "" {
		model = "claude-haiku-4-20250514"
	}
	p, err := genkit.NewAnthropicFoundryProvider(
		context.Background(), resource, model, apiKey, "", 256,
	)
	if err != nil {
		t.Fatalf("NewAnthropicFoundryProvider: %v", err)
	}
	chatRoundTrip(t, p)
}

// TestIntegration_ProviderInterface verifies that all concrete providers satisfy provider.Provider.
func TestIntegration_ProviderInterface(t *testing.T) {
	apiKey := envOrSkip(t, "ANTHROPIC_API_KEY")
	p, err := genkit.NewAnthropicProvider(context.Background(), apiKey, "claude-haiku-4-5-20251001", "", 256)
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	var _ provider.Provider = p
	if p.AuthModeInfo().Mode == "" {
		t.Error("expected non-empty AuthModeInfo.Mode")
	}
}

// TestIntegration_StreamThinkingTrace verifies thinking traces propagate via streaming.
// Uses a model that supports extended thinking (claude-sonnet or claude-3-7-sonnet).
func TestIntegration_StreamThinkingTrace(t *testing.T) {
	apiKey := envOrSkip(t, "ANTHROPIC_API_KEY")
	// claude-3-7-sonnet-20250219 supports extended thinking
	p, err := genkit.NewAnthropicProvider(context.Background(), apiKey, "claude-sonnet-4-20250514", "", 2048)
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	ctx := context.Background()
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "What is 2+2? Think step by step."}}
	ch, err := p.Stream(ctx, msgs, nil)
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	var textBuf strings.Builder
	var gotDone bool
	for ev := range ch {
		switch ev.Type {
		case "text":
			textBuf.WriteString(ev.Text)
		case "done":
			gotDone = true
		case "error":
			t.Fatalf("stream error: %s", ev.Error)
		}
	}
	if !gotDone {
		t.Error("expected done event")
	}
	if textBuf.Len() == 0 {
		t.Error("expected non-empty text response")
	}
}
