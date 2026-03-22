package executor

import (
	"testing"
)

// TestLoopDetector_DefaultConfig verifies default thresholds are applied when zero values are given.
func TestLoopDetector_DefaultConfig(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	if ld.maxConsecutive != 3 {
		t.Errorf("maxConsecutive: want 3, got %d", ld.maxConsecutive)
	}
	if ld.maxErrors != 2 {
		t.Errorf("maxErrors: want 2, got %d", ld.maxErrors)
	}
	if ld.maxAlternating != 3 {
		t.Errorf("maxAlternating: want 3, got %d", ld.maxAlternating)
	}
	if ld.maxNoProgress != 3 {
		t.Errorf("maxNoProgress: want 3, got %d", ld.maxNoProgress)
	}
}

// TestLoopDetector_OKWhenEmpty verifies no loop is detected on empty history.
func TestLoopDetector_OKWhenEmpty(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{})
	status, msg := ld.Check()
	if status != LoopStatusOK {
		t.Errorf("expected OK on empty history, got status=%d msg=%q", status, msg)
	}
}

// TestLoopDetector_ConsecutiveWarning verifies warning fires before break threshold.
func TestLoopDetector_ConsecutiveWarning(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{MaxConsecutive: 3})
	args := map[string]any{"key": "val"}
	ld.Record("tool_a", args, "result", false)
	ld.Record("tool_a", args, "result2", false)
	status, _ := ld.Check()
	if status != LoopStatusWarning {
		t.Errorf("expected Warning at consecutive=2 (maxConsecutive=3), got %d", status)
	}
}

// TestLoopDetector_ConsecutiveBreak verifies break fires at threshold.
func TestLoopDetector_ConsecutiveBreak(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{MaxConsecutive: 3})
	args := map[string]any{"key": "val"}
	for i := 0; i < 3; i++ {
		ld.Record("tool_a", args, "some_result", false)
	}
	status, msg := ld.Check()
	if status != LoopStatusBreak {
		t.Errorf("expected Break at consecutive=3, got %d", status)
	}
	if msg == "" {
		t.Error("expected non-empty break message")
	}
}

// TestLoopDetector_RepeatedError verifies error loop detection.
func TestLoopDetector_RepeatedError(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{MaxErrors: 2})
	args := map[string]any{"input": "bad"}
	ld.Record("tool_b", args, "connection refused", true)
	ld.Record("tool_b", args, "connection refused", true)
	status, msg := ld.Check()
	if status != LoopStatusBreak {
		t.Errorf("expected Break on repeated error, got %d", status)
	}
	if msg == "" {
		t.Error("expected non-empty break message")
	}
}

// TestLoopDetector_AlternatingPattern verifies A/B/A/B detection.
func TestLoopDetector_AlternatingPattern(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{MaxAlternating: 3})
	argsA := map[string]any{"op": "fetch"}
	argsB := map[string]any{"op": "store"}
	// Build 3 complete A/B cycles: A B A B A B
	for i := 0; i < 3; i++ {
		ld.Record("tool_fetch", argsA, "data", false)
		ld.Record("tool_store", argsB, "ok", false)
	}
	status, _ := ld.Check()
	if status != LoopStatusBreak {
		t.Errorf("expected Break on alternating A/B loop (3 cycles), got %d", status)
	}
}

// TestLoopDetector_NoProgressBreak verifies same tool+args+result triggers break.
func TestLoopDetector_NoProgressBreak(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{MaxNoProgress: 3})
	args := map[string]any{"query": "status"}
	for i := 0; i < 3; i++ {
		ld.Record("check_status", args, "pending", false)
	}
	status, msg := ld.Check()
	if status != LoopStatusBreak {
		t.Errorf("expected Break on no-progress (same result x3), got %d", status)
	}
	if msg == "" {
		t.Error("expected non-empty break message")
	}
}

// TestLoopDetector_Reset verifies history is cleared.
func TestLoopDetector_Reset(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{MaxConsecutive: 2})
	args := map[string]any{"x": 1}
	ld.Record("tool_x", args, "r", false)
	ld.Record("tool_x", args, "r", false)
	status, _ := ld.Check()
	if status == LoopStatusOK {
		t.Fatal("expected non-OK before reset")
	}

	ld.Reset()
	status, _ = ld.Check()
	if status != LoopStatusOK {
		t.Errorf("expected OK after Reset, got %d", status)
	}
}

// TestLoopDetector_DifferentArgsNoLoop verifies different args don't trigger consecutive loop.
func TestLoopDetector_DifferentArgsNoLoop(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{MaxConsecutive: 3})
	for i := 0; i < 5; i++ {
		ld.Record("tool_a", map[string]any{"i": i}, "result", false)
	}
	status, _ := ld.Check()
	if status != LoopStatusOK {
		t.Errorf("expected OK for calls with different args, got %d", status)
	}
}

// TestLoopDetector_CustomThresholds verifies custom config is respected.
func TestLoopDetector_CustomThresholds(t *testing.T) {
	ld := NewLoopDetector(LoopDetectorConfig{
		MaxConsecutive: 5,
		MaxErrors:      4,
		MaxAlternating: 6,
		MaxNoProgress:  5,
	})
	if ld.maxConsecutive != 5 {
		t.Errorf("maxConsecutive: want 5, got %d", ld.maxConsecutive)
	}
	if ld.maxErrors != 4 {
		t.Errorf("maxErrors: want 4, got %d", ld.maxErrors)
	}
}
