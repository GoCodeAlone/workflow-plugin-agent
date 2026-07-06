package provider

import (
	"context"
	"strings"
	"testing"
)

func TestListModelsHostedProviderVariantsRequireDynamicCatalog(t *testing.T) {
	for _, providerType := range []string{"openai_azure", "anthropic_vertex", "anthropic_foundry"} {
		t.Run(providerType, func(t *testing.T) {
			_, err := ListModels(context.Background(), providerType, "", "")
			if err == nil {
				t.Fatal("expected dynamic listing error, got nil")
			}
			if !strings.Contains(err.Error(), "dynamic model listing") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestListModelsCopilotReturnsLiveListingErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ListModels(ctx, "copilot", "bad-token", "")
	if err == nil {
		t.Fatal("expected canceled live listing error, got nil")
	}
}

func TestListModelsCohereReturnsLiveListingErrors(t *testing.T) {
	_, err := ListModels(context.Background(), "cohere", "bad-token", "http://example.com")
	if err == nil {
		t.Fatal("expected invalid base URL error, got nil")
	}
}

func TestListModelsGeminiReturnsLiveListingErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ListModels(ctx, "gemini", "bad-token", "")
	if err == nil {
		t.Fatal("expected canceled live listing error, got nil")
	}
}
