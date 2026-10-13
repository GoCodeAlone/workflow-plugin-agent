package executor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/policy"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/tools"
)

func TestExecute_PolicyBridgeNativePathDeny(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	deniedPath := filepath.Join(home, ".ssh", "id_rsa")
	allowedPath := filepath.Join(home, "notes.txt")
	if err := os.WriteFile(deniedPath, []byte("denied fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allowedPath, []byte("allowed sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The provider supplies tool calls, and the tool records real file reads.
	// Policy, its bridge, the executor, and registry run without substitutes.
	var invokedPaths []string
	reg := tools.NewRegistry()
	reg.Register(&simpleTool{
		name: "file_read",
		def: provider.ToolDef{
			Name:        "file_read",
			Description: "read a fixture file",
		},
		fn: func(_ context.Context, args map[string]any) (any, error) {
			path := args["path"].(string)
			invokedPaths = append(invokedPaths, path)
			data, err := os.ReadFile(path)
			return string(data), err
		},
	})
	te := policy.NewTrustEngine("custom", []policy.TrustRule{
		{Pattern: "file_read", Action: policy.Allow},
		{Pattern: "path:**", Action: policy.Allow},
		{Pattern: "path:~/.ssh/*", Action: policy.Deny},
	}, nil)
	chatCalls := 0
	p := &callCountProvider{
		onChat: func() (*provider.Response, error) {
			chatCalls++
			if chatCalls == 1 {
				return &provider.Response{ToolCalls: []provider.ToolCall{
					{ID: "native-denied", Name: "file_read", Arguments: map[string]any{"path": deniedPath}},
					{ID: "native-allowed", Name: "file_read", Arguments: map[string]any{"path": allowedPath}},
				}}, nil
			}
			return &provider.Response{Content: "done"}, nil
		},
	}
	var events []Event
	result, err := Execute(context.Background(), Config{
		Provider:      p,
		ToolRegistry:  reg,
		TrustEngine:   NewPolicyTrustBridge(te),
		MaxIterations: 2,
		OnEvent: func(e Event) {
			events = append(events, e)
		},
	}, "read the allowed fixture", "check native path trust", "path-regression")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" || chatCalls != 2 {
		t.Errorf("executor completion: status %q, provider calls %d; want completed after 2 calls", result.Status, chatCalls)
	}
	if len(invokedPaths) != 1 || invokedPaths[0] != allowedPath {
		t.Errorf("file_read invoked for %q, want only allowed path %q; denied native path %q must never execute", invokedPaths, allowedPath, deniedPath)
	}

	denyEvents, allowEvents := 0, 0
	toolResults := make(map[string]Event)
	for _, e := range events {
		switch e.Type {
		case EventTrustDeny:
			if e.ToolName != "file_read" {
				t.Errorf("trust deny tool = %q, want file_read", e.ToolName)
			}
			denyEvents++
		case EventTrustAllow:
			allowEvents++
		case EventToolCallResult:
			toolResults[e.ToolCallID] = e
		}
	}
	if denyEvents != 1 || allowEvents != 1 {
		t.Errorf("trust events: deny %d, allow %d; want one deny and one allowed control", denyEvents, allowEvents)
	}
	if e, ok := toolResults["native-denied"]; !ok || !e.ToolError || !strings.Contains(e.ToolResult, "denied by trust policy") {
		t.Errorf("denied tool result = %+v (present %v), want trust-policy error", e, ok)
	}
	if e, ok := toolResults["native-allowed"]; !ok || e.ToolError || e.ToolResult != `"allowed sentinel"` {
		t.Errorf("allowed tool result = %+v (present %v), want actual fixture contents", e, ok)
	}
}
