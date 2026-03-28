package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	"github.com/GoCodeAlone/workflow/module"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// ScriptedSource unit tests
// ---------------------------------------------------------------------------

func TestScriptedSource_BasicSequence(t *testing.T) {
	steps := []ScriptedStep{
		{Content: "first"},
		{Content: "second"},
		{Content: "third"},
	}
	source := NewScriptedSource(steps, false)
	ctx := context.Background()

	for i, want := range []string{"first", "second", "third"} {
		resp, err := source.GetResponse(ctx, Interaction{ID: "test"})
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if resp.Content != want {
			t.Errorf("step %d: got %q, want %q", i, resp.Content, want)
		}
	}
}

func TestScriptedSource_WithToolCalls(t *testing.T) {
	steps := []ScriptedStep{
		{
			Content: "I'll read the file.",
			ToolCalls: []provider.ToolCall{
				{ID: "call-1", Name: "file_read", Arguments: map[string]any{"path": "/tmp/test"}},
			},
		},
		{Content: "Done."},
	}
	source := NewScriptedSource(steps, false)
	ctx := context.Background()

	resp, err := source.GetResponse(ctx, Interaction{ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "file_read" {
		t.Errorf("expected file_read, got %s", resp.ToolCalls[0].Name)
	}
	if resp.ToolCalls[0].ID != "call-1" {
		t.Errorf("expected call-1, got %s", resp.ToolCalls[0].ID)
	}
}

func TestScriptedSource_Exhausted(t *testing.T) {
	steps := []ScriptedStep{{Content: "only one"}}
	source := NewScriptedSource(steps, false)
	ctx := context.Background()

	// First call succeeds
	_, err := source.GetResponse(ctx, Interaction{ID: "test"})
	if err != nil {
		t.Fatal(err)
	}

	// Second call should error
	_, err = source.GetResponse(ctx, Interaction{ID: "test"})
	if err == nil {
		t.Fatal("expected error on exhausted source")
	}
}

func TestScriptedSource_Loop(t *testing.T) {
	steps := []ScriptedStep{
		{Content: "a"},
		{Content: "b"},
	}
	source := NewScriptedSource(steps, true)
	ctx := context.Background()

	// Should cycle: a, b, a, b, a
	expected := []string{"a", "b", "a", "b", "a"}
	for i, want := range expected {
		resp, err := source.GetResponse(ctx, Interaction{ID: "test"})
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if resp.Content != want {
			t.Errorf("step %d: got %q, want %q", i, resp.Content, want)
		}
	}
}

func TestScriptedSource_Delay(t *testing.T) {
	steps := []ScriptedStep{
		{Content: "delayed", Delay: 50 * time.Millisecond},
	}
	source := NewScriptedSource(steps, false)
	ctx := context.Background()

	start := time.Now()
	resp, err := source.GetResponse(ctx, Interaction{ID: "test"})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "delayed" {
		t.Errorf("got %q, want %q", resp.Content, "delayed")
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("delay too short: %v", elapsed)
	}
}

func TestScriptedSource_ErrorInjection(t *testing.T) {
	steps := []ScriptedStep{
		{Error: "simulated failure"},
	}
	source := NewScriptedSource(steps, false)
	ctx := context.Background()

	resp, err := source.GetResponse(ctx, Interaction{ID: "test"})
	if err != nil {
		t.Fatalf("GetResponse should not return err, got: %v", err)
	}
	if resp.Error != "simulated failure" {
		t.Errorf("got error %q, want %q", resp.Error, "simulated failure")
	}
}

func TestScriptedSource_SummarizationAutoRespond(t *testing.T) {
	steps := []ScriptedStep{
		{Content: "only step"},
	}
	source := NewScriptedSource(steps, false)
	ctx := context.Background()

	// Send a summarization request — should NOT consume a step
	interaction := Interaction{
		ID: "test",
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "You are a precise summariser. Produce a concise factual summary."},
			{Role: provider.RoleUser, Content: "Summarise this conversation."},
		},
	}
	resp, err := source.GetResponse(ctx, interaction)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content == "only step" {
		t.Error("summarization should not consume a scripted step")
	}

	// Original step should still be available
	if source.Remaining() != 1 {
		t.Errorf("expected 1 remaining step, got %d", source.Remaining())
	}
}

func TestScriptedSource_ConcurrentAccess(t *testing.T) {
	steps := make([]ScriptedStep, 100)
	for i := range steps {
		steps[i] = ScriptedStep{Content: "step"}
	}
	source := NewScriptedSource(steps, false)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = source.GetResponse(ctx, Interaction{ID: "test"})
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// ChannelSource unit tests
// ---------------------------------------------------------------------------

func TestChannelSource_BasicFlow(t *testing.T) {
	source, interactionsCh, responsesCh := NewChannelSource()
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		interaction := <-interactionsCh
		if interaction.ID != "test-123" {
			t.Errorf("expected ID test-123, got %s", interaction.ID)
		}
		responsesCh <- InteractionResponse{Content: "channel response"}
	}()

	resp, err := source.GetResponse(ctx, Interaction{ID: "test-123"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "channel response" {
		t.Errorf("got %q, want %q", resp.Content, "channel response")
	}
	wg.Wait()
}

func TestChannelSource_Timeout(t *testing.T) {
	source, _, _ := NewChannelSource()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := source.GetResponse(ctx, Interaction{ID: "test"})
	if err == nil {
		t.Fatal("expected error on timeout")
	}
}

// ---------------------------------------------------------------------------
// HTTPSource unit tests
// ---------------------------------------------------------------------------

func TestHTTPSource_ListPending(t *testing.T) {
	source := NewHTTPSource(nil)

	// Start a goroutine that will add a pending interaction
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = source.GetResponse(ctx, Interaction{
			ID:        "int-1",
			CreatedAt: time.Now(),
		})
	}()

	// Wait for interaction to be registered
	time.Sleep(50 * time.Millisecond)

	summaries := source.ListPending()
	if len(summaries) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(summaries))
	}
	if summaries[0].ID != "int-1" {
		t.Errorf("expected ID int-1, got %s", summaries[0].ID)
	}

	// Clean up by responding
	_ = source.Respond("int-1", InteractionResponse{Content: "ok"})
	wg.Wait()
}

func TestHTTPSource_GetAndRespond(t *testing.T) {
	source := NewHTTPSource(nil)
	ctx := context.Background()

	var resp *InteractionResponse
	var respErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, respErr = source.GetResponse(ctx, Interaction{
			ID: "int-2",
			Messages: []provider.Message{
				{Role: provider.RoleUser, Content: "hello"},
			},
			Tools: []provider.ToolDef{
				{Name: "file_read"},
			},
		})
	}()

	// Wait for interaction to appear
	time.Sleep(50 * time.Millisecond)

	// Get full interaction details
	interaction, err := source.GetInteraction("int-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(interaction.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(interaction.Messages))
	}
	if len(interaction.Tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(interaction.Tools))
	}

	// Submit response
	err = source.Respond("int-2", InteractionResponse{Content: "I'll read the file."})
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	if respErr != nil {
		t.Fatalf("GetResponse error: %v", respErr)
	}
	if resp.Content != "I'll read the file." {
		t.Errorf("got %q, want %q", resp.Content, "I'll read the file.")
	}
}

func TestHTTPSource_RespondNotFound(t *testing.T) {
	source := NewHTTPSource(nil)
	err := source.Respond("nonexistent", InteractionResponse{Content: "x"})
	if err == nil {
		t.Fatal("expected error for nonexistent interaction")
	}
}

func TestHTTPSource_ContextCancellation(t *testing.T) {
	source := NewHTTPSource(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := source.GetResponse(ctx, Interaction{ID: "int-cancel"})
	if err == nil {
		t.Fatal("expected error on cancellation")
	}

	// Should be cleaned up from pending
	if source.PendingCount() != 0 {
		t.Errorf("expected 0 pending after cancellation, got %d", source.PendingCount())
	}
}

func TestHTTPSource_WithSSEHub(t *testing.T) {
	hub := &SSEHub{
		name:    "test",
		path:    "/events",
		clients: make(map[chan []byte]struct{}),
	}
	source := NewHTTPSource(hub)

	// Add an SSE client to capture events
	sseCh := make(chan []byte, 16)
	hub.mu.Lock()
	hub.clients[sseCh] = struct{}{}
	hub.mu.Unlock()

	// Start interaction in background
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = source.GetResponse(ctx, Interaction{
			ID:        "sse-test",
			CreatedAt: time.Now(),
		})
	}()

	// Should receive SSE event
	select {
	case msg := <-sseCh:
		msgStr := string(msg)
		if len(msgStr) == 0 {
			t.Error("empty SSE message")
		}
	case <-time.After(time.Second):
		t.Error("timed out waiting for SSE event")
	}

	// Clean up
	_ = source.Respond("sse-test", InteractionResponse{Content: "ok"})
}

// ---------------------------------------------------------------------------
// LoadScenario unit test
// ---------------------------------------------------------------------------

func TestLoadScenario(t *testing.T) {
	// Create a temp scenario file
	dir := t.TempDir()
	path := filepath.Join(dir, "test-scenario.yaml")
	content := `name: test-scenario
description: A test scenario
loop: true
steps:
  - content: "Step one"
  - content: "Step two with tool call"
    tool_calls:
      - name: file_read
        arguments:
          path: "/tmp/test"
  - content: "Done"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	scenario, err := LoadScenario(path)
	if err != nil {
		t.Fatal(err)
	}
	if scenario.Name != "test-scenario" {
		t.Errorf("name: %q", scenario.Name)
	}
	if !scenario.Loop {
		t.Error("expected loop to be true")
	}
	if len(scenario.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(scenario.Steps))
	}
	if len(scenario.Steps[1].ToolCalls) != 1 {
		t.Errorf("expected 1 tool call in step 2, got %d", len(scenario.Steps[1].ToolCalls))
	}
}

func TestLoadScenario_FileNotFound(t *testing.T) {
	_, err := LoadScenario("/nonexistent/scenario.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadScenario_EmptySteps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(path, []byte("name: empty\nsteps: []\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadScenario(path)
	if err == nil {
		t.Fatal("expected error for empty steps")
	}
}

// ---------------------------------------------------------------------------
// TestProvider unit tests
// ---------------------------------------------------------------------------

func TestTestProvider_Name(t *testing.T) {
	source := NewScriptedSource([]ScriptedStep{{Content: "x"}}, false)
	tp := NewTestProvider(source)
	if tp.Name() != "test" {
		t.Errorf("expected 'test', got %q", tp.Name())
	}

	tp2 := NewTestProvider(source, WithName("custom"))
	if tp2.Name() != "custom" {
		t.Errorf("expected 'custom', got %q", tp2.Name())
	}
}

func TestTestProvider_Chat(t *testing.T) {
	source := NewScriptedSource([]ScriptedStep{
		{Content: "hello from test provider"},
	}, false)
	tp := NewTestProvider(source)

	resp, err := tp.Chat(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello from test provider" {
		t.Errorf("got %q", resp.Content)
	}
	if tp.InteractionCount() != 1 {
		t.Errorf("expected 1 interaction, got %d", tp.InteractionCount())
	}
}

func TestTestProvider_Chat_WithToolCalls(t *testing.T) {
	source := NewScriptedSource([]ScriptedStep{
		{
			Content: "reading file",
			ToolCalls: []provider.ToolCall{
				{Name: "file_read", Arguments: map[string]any{"path": "/tmp/test"}},
			},
		},
	}, false)
	tp := NewTestProvider(source)

	resp, err := tp.Chat(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	// Should auto-generate ID
	if resp.ToolCalls[0].ID == "" {
		t.Error("expected auto-generated tool call ID")
	}
}

func TestTestProvider_Chat_ErrorInjection(t *testing.T) {
	source := NewScriptedSource([]ScriptedStep{
		{Error: "boom"},
	}, false)
	tp := NewTestProvider(source)

	_, err := tp.Chat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTestProvider_Stream(t *testing.T) {
	source := NewScriptedSource([]ScriptedStep{
		{Content: "streamed content"},
	}, false)
	tp := NewTestProvider(source)

	ch, err := tp.Stream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	var events []provider.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != "text" || events[0].Text != "streamed content" {
		t.Errorf("first event: %+v", events[0])
	}
	if events[1].Type != "done" {
		t.Errorf("last event type: %s", events[1].Type)
	}
}

func TestTestProvider_Stream_WithToolCalls(t *testing.T) {
	source := NewScriptedSource([]ScriptedStep{
		{
			Content: "working",
			ToolCalls: []provider.ToolCall{
				{ID: "tc1", Name: "file_read", Arguments: map[string]any{"path": "/tmp"}},
			},
		},
	}, false)
	tp := NewTestProvider(source)

	ch, err := tp.Stream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	var events []provider.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	// text + tool_call + done = 3 events
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[1].Type != "tool_call" {
		t.Errorf("expected tool_call event, got %s", events[1].Type)
	}
}

// ---------------------------------------------------------------------------
// TestHarness unit test
// ---------------------------------------------------------------------------

func TestTestHarness_BasicFlow(t *testing.T) {
	harness := NewTestHarness(t)
	ctx := context.Background()

	go func() {
		interaction := harness.ExpectInteraction(2 * time.Second)
		if len(interaction.Messages) == 0 {
			t.Error("expected messages in interaction")
		}
		harness.RespondText("harness response")
	}()

	resp, err := harness.Provider.Chat(ctx, []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "harness response" {
		t.Errorf("got %q", resp.Content)
	}
}

// ---------------------------------------------------------------------------
// TestInteractStep unit test
// ---------------------------------------------------------------------------

func TestTestInteractStep_NoHTTPSource(t *testing.T) {
	app := newMockApp()
	step := &TestInteractStep{
		name:      "test-step",
		operation: "list_pending",
		app:       app,
	}

	pc := &module.PipelineContext{Current: map[string]any{}}
	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["success"] != false {
		t.Error("expected success=false when no HTTP source")
	}
}

func TestTestInteractStep_ListPending(t *testing.T) {
	source := NewHTTPSource(nil)
	app := newMockApp()
	app.services["ratchet-test-http-source"] = source

	step := &TestInteractStep{
		name:      "test-step",
		operation: "list_pending",
		app:       app,
	}

	pc := &module.PipelineContext{Current: map[string]any{}}
	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["success"] != true {
		t.Errorf("expected success=true, got %v", result.Output)
	}
	if result.Output["count"] != 0 {
		t.Errorf("expected 0 pending, got %v", result.Output["count"])
	}
}

// ---------------------------------------------------------------------------
// E2E integration tests (using real AgentExecuteStep)
// ---------------------------------------------------------------------------

func TestE2E_HappyPath(t *testing.T) {
	// Create a temp workspace with a file for the agent to read/write
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	source := NewScriptedSource([]ScriptedStep{
		{
			Content: "I'll read the input file.",
			ToolCalls: []provider.ToolCall{
				{ID: "tc1", Name: "file_read", Arguments: map[string]any{"path": "input.txt"}},
			},
		},
		{
			Content: "Now I'll write the output.",
			ToolCalls: []provider.ToolCall{
				{ID: "tc2", Name: "file_write", Arguments: map[string]any{
					"path":    "output.txt",
					"content": "processed: hello world",
				}},
			},
		},
		{Content: "Task complete. I read the input and wrote the output."},
	}, false)

	tp := NewTestProvider(source)
	env := SetupE2EAgent(t, tp)

	// Register tools with workspace and permissive policy engine.
	env.ToolRegistry = NewToolRegistry()
	env.ToolRegistry.SetPolicyEngine(&ToolPolicyEngine{DefaultPolicy: PolicyAllow})
	env.ToolRegistry.Register(&tools.FileReadTool{Workspace: workspace})
	env.ToolRegistry.Register(&tools.FileWriteTool{Workspace: workspace})
	env.ToolRegistry.Register(&tools.FileListTool{Workspace: workspace})
	env.App.services["ratchet-tool-registry"] = env.ToolRegistry

	pc := NewPipelineContext("test-agent", "task-1", "Read input.txt and write output.txt")

	result, err := env.Step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Output["status"] != "completed" {
		t.Errorf("expected completed, got %v", result.Output["status"])
	}
	if result.Output["iterations"] != 3 {
		t.Errorf("expected 3 iterations, got %v", result.Output["iterations"])
	}

	// Verify the file was actually written
	data, err := os.ReadFile(filepath.Join(workspace, "output.txt"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "processed: hello world" {
		t.Errorf("unexpected output: %q", string(data))
	}
}

func TestE2E_ToolError(t *testing.T) {
	source := NewScriptedSource([]ScriptedStep{
		{
			Content: "I'll try to read a nonexistent file.",
			ToolCalls: []provider.ToolCall{
				{ID: "tc1", Name: "file_read", Arguments: map[string]any{"path": "/nonexistent/file.txt"}},
			},
		},
		{Content: "The file doesn't exist. I'll report the error."},
	}, false)

	tp := NewTestProvider(source)
	env := SetupE2EAgent(t, tp)
	pc := NewPipelineContext("test-agent", "task-err", "Read a file")

	result, err := env.Step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Agent should complete (tool error is reported back, agent handles it)
	if result.Output["status"] != "completed" {
		t.Errorf("expected completed, got %v", result.Output["status"])
	}
}

func TestE2E_LoopDetection(t *testing.T) {
	// Agent makes the same tool call repeatedly — loop detector should fire
	source := NewScriptedSource([]ScriptedStep{
		{
			Content: "Check status.",
			ToolCalls: []provider.ToolCall{
				{ID: "tc1", Name: "file_list", Arguments: map[string]any{"path": "/tmp"}},
			},
		},
	}, true) // loop=true so it keeps providing the same step

	tp := NewTestProvider(source)
	env := SetupE2EAgent(t, tp)
	env.Step.maxIterations = 20 // allow enough iterations for loop detector to fire
	pc := NewPipelineContext("test-agent", "task-loop", "Check something")

	result, err := env.Step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Output["status"] != "loop_detected" {
		t.Errorf("expected loop_detected, got %v", result.Output["status"])
	}
}

func TestE2E_MaxIterations(t *testing.T) {
	// Create many steps to hit the iteration limit
	steps := make([]ScriptedStep, 20)
	for i := range steps {
		steps[i] = ScriptedStep{
			Content: "working...",
			ToolCalls: []provider.ToolCall{
				{ID: "tc", Name: "file_list", Arguments: map[string]any{"path": "/tmp/unique-" + string(rune('a'+i))}},
			},
		}
	}
	source := NewScriptedSource(steps, false)

	tp := NewTestProvider(source)
	env := SetupE2EAgent(t, tp)
	env.Step.maxIterations = 3 // very low limit
	pc := NewPipelineContext("test-agent", "task-max", "Do a lot of work")

	result, err := env.Step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	iterations, _ := result.Output["iterations"].(int)
	if iterations > 3 {
		t.Errorf("expected at most 3 iterations, got %d", iterations)
	}
}

func TestE2E_ProviderError(t *testing.T) {
	source := NewScriptedSource([]ScriptedStep{
		{Error: "provider crashed"},
	}, false)

	tp := NewTestProvider(source)
	env := SetupE2EAgent(t, tp)
	pc := NewPipelineContext("test-agent", "task-fail", "Do something")

	result, err := env.Step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Output["status"] != "failed" {
		t.Errorf("expected failed, got %v", result.Output["status"])
	}
}

func TestE2E_TranscriptRecording(t *testing.T) {
	source := NewScriptedSource([]ScriptedStep{
		{
			Content: "I'll list the files.",
			ToolCalls: []provider.ToolCall{
				{ID: "tc1", Name: "file_list", Arguments: map[string]any{"path": "/tmp"}},
			},
		},
		{Content: "Done listing files."},
	}, false)

	tp := NewTestProvider(source)
	env := SetupE2EAgent(t, tp)
	pc := NewPipelineContext("test-agent", "task-transcript", "List some files")

	_, err := env.Step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Verify transcripts were recorded
	entries, err := env.Recorder.GetByTask(context.Background(), "task-transcript")
	if err != nil {
		t.Fatalf("GetByTask: %v", err)
	}

	// Expected entries: system, user, assistant(with tool calls), tool result, assistant(final)
	if len(entries) < 4 {
		t.Errorf("expected at least 4 transcript entries, got %d", len(entries))
		for i, e := range entries {
			t.Logf("  entry %d: role=%s content=%q", i, e.Role, e.Content[:min(50, len(e.Content))])
		}
	}

	// Check roles present
	roles := make(map[provider.Role]int)
	for _, e := range entries {
		roles[e.Role]++
	}
	if roles[provider.RoleSystem] == 0 {
		t.Error("no system transcript entry")
	}
	if roles[provider.RoleAssistant] == 0 {
		t.Error("no assistant transcript entry")
	}
	if roles[provider.RoleTool] == 0 {
		t.Error("no tool transcript entry")
	}
}

func TestE2E_ChannelMode(t *testing.T) {
	harness := NewTestHarness(t)
	env := SetupE2EAgent(t, harness.Provider)

	// Set up workspace with a test file
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "test.txt"), []byte("test data"), 0644); err != nil {
		t.Fatal(err)
	}
	env.ToolRegistry = NewToolRegistry()
	env.ToolRegistry.Register(&tools.FileReadTool{Workspace: workspace})
	env.ToolRegistry.Register(&tools.FileWriteTool{Workspace: workspace})
	env.ToolRegistry.Register(&tools.FileListTool{Workspace: workspace})
	env.App.services["ratchet-tool-registry"] = env.ToolRegistry

	pc := NewPipelineContext("test-agent", "task-channel", "Do an interactive task")

	var result *module.StepResult
	var execErr error
	done := make(chan struct{})

	go func() {
		defer close(done)
		result, execErr = env.Step.Execute(context.Background(), pc)
	}()

	// First interaction: agent asks to read a file
	interaction1 := harness.ExpectInteraction(5 * time.Second)
	if len(interaction1.Messages) < 2 {
		t.Fatalf("expected at least 2 messages (system + user), got %d", len(interaction1.Messages))
	}

	harness.RespondToolCall("I'll read the file.", provider.ToolCall{
		ID:        "tc1",
		Name:      "file_read",
		Arguments: map[string]any{"path": "test.txt"},
	})

	// Second interaction: tool result is in messages, agent responds with final answer
	interaction2 := harness.ExpectInteraction(5 * time.Second)
	// Should have tool result in messages now
	hasToolResult := false
	for _, m := range interaction2.Messages {
		if m.Role == provider.RoleTool {
			hasToolResult = true
		}
	}
	if !hasToolResult {
		t.Error("expected tool result in second interaction messages")
	}

	harness.RespondText("File read successfully. Task complete.")

	<-done
	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}
	if result.Output["status"] != "completed" {
		t.Errorf("expected completed, got %v", result.Output["status"])
	}
}

func TestE2E_ApprovalGate(t *testing.T) {
	// Set up approval manager
	source := NewScriptedSource([]ScriptedStep{
		{
			Content: "I need approval to proceed.",
			ToolCalls: []provider.ToolCall{
				{ID: "tc1", Name: "request_approval", Arguments: map[string]any{
					"action": "deploy",
					"reason": "deploying to production",
				}},
			},
		},
		{Content: "Approval received. Deploying now."},
	}, false)

	tp := NewTestProvider(source)
	env := SetupE2EAgent(t, tp)

	// Register approval manager
	db := env.DB
	for _, ddl := range []string{createApprovalsTable} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create approvals table: %v", err)
		}
	}
	am := NewApprovalManager(db)

	// Wire in SSE hub for notifications
	hub := &SSEHub{
		name:    "test-hub",
		path:    "/events",
		clients: make(map[chan []byte]struct{}),
	}
	am.SetSSEHub(hub)
	env.App.services["ratchet-approval-manager"] = am

	// Register the approval tool
	env.ToolRegistry.Register(&requestApprovalTestTool{Manager: am})

	pc := NewPipelineContext("test-agent", "task-approval", "Deploy to production")

	var result *module.StepResult
	var execErr error
	done := make(chan struct{})

	go func() {
		defer close(done)
		result, execErr = env.Step.Execute(context.Background(), pc)
	}()

	// Wait for the approval to be created, then approve it
	time.Sleep(200 * time.Millisecond)

	// Find the pending approval
	rows, err := db.Query("SELECT id FROM approvals WHERE status = 'pending' LIMIT 1")
	if err != nil {
		t.Fatalf("query approvals: %v", err)
	}
	var approvalID string
	if rows.Next() {
		_ = rows.Scan(&approvalID)
	}
	_ = rows.Close()

	if approvalID != "" {
		if err := am.Approve(context.Background(), approvalID, "approved for test"); err != nil {
			t.Fatalf("approve: %v", err)
		}
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for agent execution to complete")
	}

	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}
	if result.Output["status"] != "completed" {
		t.Errorf("expected completed, got %v", result.Output)
	}
}

// requestApprovalTestTool is a simplified version for tests.
type requestApprovalTestTool struct {
	Manager *ApprovalManager
}

func (t *requestApprovalTestTool) Name() string        { return "request_approval" }
func (t *requestApprovalTestTool) Description() string { return "Request human approval" }
func (t *requestApprovalTestTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "request_approval",
		Description: "Request human approval for an action",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string"},
				"reason": map[string]any{"type": "string"},
			},
		},
	}
}
func (t *requestApprovalTestTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	action, _ := args["action"].(string)
	reason, _ := args["reason"].(string)
	id, err := t.Manager.CreateApproval(ctx, "", "", action, reason, "")
	if err != nil {
		return nil, err
	}
	return map[string]any{"approval_id": id, "status": "pending"}, nil
}
