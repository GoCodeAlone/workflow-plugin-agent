package provider

import "testing"

func TestAllAuthModesIncludesOpenAIChatGPT(t *testing.T) {
	for _, mode := range AllAuthModes() {
		if mode.Mode != "chatgpt" {
			continue
		}
		if mode.ServerSafe {
			t.Fatal("chatgpt auth mode must not be marked server-safe")
		}
		if mode.DocsURL == "" {
			t.Fatal("chatgpt auth mode should link official docs")
		}
		return
	}
	t.Fatal("missing chatgpt auth mode")
}

func TestListModelsOpenAIChatGPT(t *testing.T) {
	models, err := ListModels(t.Context(), "openai_chatgpt", "", "")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5-codex" {
		t.Fatalf("models = %+v", models)
	}
}
