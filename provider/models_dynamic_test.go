package provider

import (
	"context"
	"io"
	"net/http"
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

func TestOpenAICompatibleModelListingKeepsProviderReturnedIDs(t *testing.T) {
	origClient := modelHTTPClient
	modelHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://example.com/v1/models" {
			t.Fatalf("request URL = %s", r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"llama-3.3-70b"},{"id":"provider-custom-chat"}]}`)),
		}, nil
	})}
	t.Cleanup(func() { modelHTTPClient = origClient })

	models, err := ListModels(t.Context(), "openai_compatible", "test-key", "https://example.com")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ID != "llama-3.3-70b" || models[1].ID != "provider-custom-chat" {
		t.Fatalf("models = %+v", models)
	}
}

func TestCustomModelListingUsesAnthropicCompatibilityWhenConfigured(t *testing.T) {
	origClient := modelHTTPClient
	modelHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://example.com/v1/models" {
			t.Fatalf("request URL = %s", r.URL.String())
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Fatalf("missing anthropic-version header")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"claude-compatible","display_name":"Claude Compatible","type":"model"}]}`)),
		}, nil
	})}
	t.Cleanup(func() { modelHTTPClient = origClient })

	models, err := ListModelsWithSettings(t.Context(), "custom", "test-key", "https://example.com", map[string]string{"api_compat": "anthropic"})
	if err != nil {
		t.Fatalf("ListModelsWithSettings: %v", err)
	}
	if len(models) != 1 || models[0].ID != "claude-compatible" {
		t.Fatalf("models = %+v", models)
	}
}
