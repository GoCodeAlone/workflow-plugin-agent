package orchestrator

import (
	"fmt"
	"testing"
)

func TestLoopDetector_EmptyHistory(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	status, msg := ld.Check()
	if status != LoopStatusOK {
		t.Errorf("empty history: expected OK, got %v (%s)", status, msg)
	}
}

func TestLoopDetector_SingleEntry(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	ld.Record("read_file", map[string]any{"path": "/tmp/foo"}, "contents", false)
	status, msg := ld.Check()
	if status != LoopStatusOK {
		t.Errorf("single entry: expected OK, got %v (%s)", status, msg)
	}
}

func TestLoopDetector_DifferentTools_OK(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	ld.Record("read_file", map[string]any{"path": "/tmp/a"}, "data", false)
	ld.Record("write_file", map[string]any{"path": "/tmp/b"}, "ok", false)
	ld.Record("list_dir", map[string]any{"path": "/tmp"}, "[]", false)
	status, msg := ld.Check()
	if status != LoopStatusOK {
		t.Errorf("different tools: expected OK, got %v (%s)", status, msg)
	}
}

// --- Strategy 1: consecutive identical calls ---

func TestLoopDetector_Consecutive_Warning(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	args := map[string]any{"path": "/tmp/foo"}
	ld.Record("read_file", args, "data", false)
	ld.Record("read_file", args, "data", false)
	status, msg := ld.Check()
	if status != LoopStatusWarning {
		t.Errorf("2 consecutive: expected Warning, got %v (%s)", status, msg)
	}
}

func TestLoopDetector_Consecutive_Break(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	args := map[string]any{"path": "/tmp/foo"}
	ld.Record("read_file", args, "data", false)
	ld.Record("read_file", args, "data", false)
	ld.Record("read_file", args, "data", false)
	status, msg := ld.Check()
	if status != LoopStatusBreak {
		t.Errorf("3 consecutive: expected Break, got %v (%s)", status, msg)
	}
}

func TestLoopDetector_Consecutive_Reset_By_DifferentTool(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	args := map[string]any{"path": "/tmp/foo"}
	ld.Record("read_file", args, "data", false)
	ld.Record("read_file", args, "data", false)
	// Different tool resets the consecutive run
	ld.Record("write_file", map[string]any{"path": "/tmp/bar"}, "ok", false)
	status, msg := ld.Check()
	if status != LoopStatusOK {
		t.Errorf("consecutive reset by different tool: expected OK, got %v (%s)", status, msg)
	}
}

func TestLoopDetector_Consecutive_DifferentArgs_OK(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	ld.Record("read_file", map[string]any{"path": "/tmp/a"}, "data", false)
	ld.Record("read_file", map[string]any{"path": "/tmp/b"}, "data", false)
	ld.Record("read_file", map[string]any{"path": "/tmp/c"}, "data", false)
	status, msg := ld.Check()
	if status != LoopStatusOK {
		t.Errorf("same tool different args: expected OK, got %v (%s)", status, msg)
	}
}

// --- Strategy 2: repeated error pattern ---

func TestLoopDetector_RepeatedError_Break(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	args := map[string]any{"path": "/nonexistent"}
	errMsg := "file not found"
	ld.Record("read_file", args, errMsg, true)
	ld.Record("read_file", args, errMsg, true)
	status, msg := ld.Check()
	if status != LoopStatusBreak {
		t.Errorf("2 identical errors: expected Break, got %v (%s)", status, msg)
	}
}

func TestLoopDetector_RepeatedError_DifferentErrors_OK(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	args := map[string]any{"path": "/nonexistent"}
	ld.Record("read_file", args, "file not found", true)
	ld.Record("read_file", args, "permission denied", true)
	status, msg := ld.Check()
	// Different error messages — no repeated error pattern
	if status == LoopStatusBreak {
		t.Errorf("different errors: should not be Break, got %v (%s)", status, msg)
	}
}

func TestLoopDetector_RepeatedError_NonError_NoBreak(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	args := map[string]any{"path": "/tmp/foo"}
	ld.Record("read_file", args, "data", false)
	ld.Record("read_file", args, "data", false)
	// Third call — consecutive break takes precedence, but also ensure no
	// false repeated-error trigger when isError=false.
	// Test with only 2 non-error calls: should be Warning (consecutive), not Break (error).
	ld2 := NewLoopDetector(LoopDetectorConfig{})
	ld2.Record("read_file", args, "data", false)
	ld2.Record("read_file", args, "data", false)
	status, _ := ld2.Check()
	if status == LoopStatusBreak {
		// Could be either Warning (consecutive) — Break is wrong here
		// Actually with 2 consecutive we expect Warning, not Break
		t.Errorf("2 non-error consecutive: expected Warning not Break from error strategy")
	}
	_ = ld
}

// --- Strategy 3: alternating A/B/A/B pattern ---

func TestLoopDetector_Alternating_Break(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	argsA := map[string]any{"tool": "a"}
	argsB := map[string]any{"tool": "b"}
	// 3 full cycles = 6 entries
	for i := 0; i < 3; i++ {
		ld.Record("tool_a", argsA, "result_a", false)
		ld.Record("tool_b", argsB, "result_b", false)
	}
	status, msg := ld.Check()
	if status != LoopStatusBreak {
		t.Errorf("3 A/B cycles: expected Break, got %v (%s)", status, msg)
	}
}

func TestLoopDetector_Alternating_TwoCycles_OK(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	argsA := map[string]any{"tool": "a"}
	argsB := map[string]any{"tool": "b"}
	// Only 2 full cycles — should not trigger
	for i := 0; i < 2; i++ {
		ld.Record("tool_a", argsA, "result_a", false)
		ld.Record("tool_b", argsB, "result_b", false)
	}
	status, msg := ld.Check()
	if status == LoopStatusBreak {
		t.Errorf("2 A/B cycles: should not break, got %v (%s)", status, msg)
	}
}

func TestLoopDetector_Alternating_ThreeToolsNoPattern(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	// A/B/C pattern with distinct results each call — not a 2-tool alternation.
	// Use a counter to vary results so no-progress doesn't fire.
	n := 0
	for i := 0; i < 3; i++ {
		n++
		ld.Record("tool_a", map[string]any{"x": 1, "n": n}, fmt.Sprintf("r%d", n), false)
		n++
		ld.Record("tool_b", map[string]any{"x": 2, "n": n}, fmt.Sprintf("r%d", n), false)
		n++
		ld.Record("tool_c", map[string]any{"x": 3, "n": n}, fmt.Sprintf("r%d", n), false)
	}
	status, msg := ld.Check()
	if status == LoopStatusBreak {
		t.Errorf("A/B/C rotation should not trigger break: %v (%s)", status, msg)
	}
}

// --- Strategy 4: no progress (identical result repeated) ---

func TestLoopDetector_NoProgress_Break(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	args := map[string]any{"query": "SELECT 1"}
	result := `{"rows":[]}`
	ld.Record("db_query", args, result, false)
	ld.Record("db_query", args, result, false)
	ld.Record("db_query", args, result, false)
	status, msg := ld.Check()
	if status != LoopStatusBreak {
		t.Errorf("no progress (3x same result): expected Break, got %v (%s)", status, msg)
	}
}

func TestLoopDetector_NoProgress_DifferentResults_OK(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	// Each call has different args (e.g. incrementing sequence number), so it is
	// not considered a repeated identical call even if the tool name is the same.
	ld.Record("poll", map[string]any{"seq": 1}, `{"status":"pending"}`, false)
	ld.Record("poll", map[string]any{"seq": 2}, `{"status":"running"}`, false)
	ld.Record("poll", map[string]any{"seq": 3}, `{"status":"done"}`, false)
	status, msg := ld.Check()
	if status != LoopStatusOK {
		t.Errorf("different args and results: expected OK, got %v (%s)", status, msg)
	}
}

// --- Reset ---

func TestLoopDetector_Reset(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	args := map[string]any{"path": "/tmp/foo"}
	ld.Record("read_file", args, "data", false)
	ld.Record("read_file", args, "data", false)
	ld.Record("read_file", args, "data", false)
	status, _ := ld.Check()
	if status != LoopStatusBreak {
		t.Fatal("expected Break before reset")
	}

	ld.Reset()
	status, msg := ld.Check()
	if status != LoopStatusOK {
		t.Errorf("after reset: expected OK, got %v (%s)", status, msg)
	}
}
