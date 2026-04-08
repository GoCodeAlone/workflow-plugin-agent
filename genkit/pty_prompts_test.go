package genkit

import (
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/policy"
)

func TestPromptHandlerTrustDialog(t *testing.T) {
	te := policy.NewTrustEngine("permissive", nil, nil)
	ph := NewPromptHandler(te, nil, nil)

	screen := "Do you trust this folder?\n  Yes, allow access\n  No\nEnter to confirm"
	action, response := ph.Evaluate(screen)
	if action != PromptActionRespond {
		t.Errorf("expected Respond, got %v", action)
	}
	if response != "\r" {
		t.Errorf("expected carriage return, got %q", response)
	}
}

func TestPromptHandlerCommandExec(t *testing.T) {
	rules := []policy.TrustRule{
		{Pattern: "bash:git *", Action: policy.Allow},
	}
	te := policy.NewTrustEngine("custom", rules, nil)
	ph := NewPromptHandler(te, nil, nil)

	screen := "Run command: git status? (y/n)"
	action, _ := ph.Evaluate(screen)
	if action != PromptActionRespond {
		t.Errorf("expected Respond for allowed command, got %v", action)
	}
}

func TestPromptHandlerDeniedCommand(t *testing.T) {
	rules := []policy.TrustRule{
		{Pattern: "bash:rm -rf *", Action: policy.Deny},
	}
	te := policy.NewTrustEngine("custom", rules, nil)
	ph := NewPromptHandler(te, nil, nil)

	screen := "Run command: rm -rf /? (y/n)"
	action, response := ph.Evaluate(screen)
	if action != PromptActionRespond {
		t.Errorf("expected Respond (with 'n'), got %v", action)
	}
	if response != "n\r" {
		t.Errorf("expected 'n\\r' for denied command, got %q", response)
	}
}

func TestPromptHandlerAskQueues(t *testing.T) {
	te := policy.NewTrustEngine("locked", nil, nil)
	var queued string
	ph := NewPromptHandler(te, nil, func(agentName, promptText string) {
		queued = promptText
	})

	screen := "Allow write to /workspace/main.go? (y/n)"
	action, _ := ph.Evaluate(screen)
	if action != PromptActionQueue {
		t.Errorf("expected Queue, got %v", action)
	}
	if queued == "" {
		t.Error("expected onQueue callback to fire")
	}
}

func TestPromptHandlerNoMatch(t *testing.T) {
	te := policy.NewTrustEngine("permissive", nil, nil)
	ph := NewPromptHandler(te, nil, nil)

	screen := "Hello, world! This is normal output."
	action, _ := ph.Evaluate(screen)
	if action != PromptActionNone {
		t.Errorf("expected None, got %v", action)
	}
}

func TestPromptHandlerCustomPattern(t *testing.T) {
	te := policy.NewTrustEngine("permissive", nil, nil)
	custom := []PromptPattern{
		{
			Name:    "npm_install",
			MatchRe: "npm install",
			Default: policy.Allow,
		},
	}
	ph := NewPromptHandler(te, custom, nil)

	screen := "npm install detected, proceed? (y/n)"
	action, response := ph.Evaluate(screen)
	if action != PromptActionRespond {
		t.Errorf("expected Respond, got %v", action)
	}
	if response != "y\r" {
		t.Errorf("expected 'y\\r', got %q", response)
	}
}
