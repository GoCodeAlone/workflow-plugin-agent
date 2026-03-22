package task

import (
	"encoding/json"
	"testing"
	"time"
)

// TestStatusValues verifies all Status constants are non-empty and distinct.
func TestStatusValues(t *testing.T) {
	statuses := []Status{
		StatusPending, StatusAssigned, StatusInProgress,
		StatusCompleted, StatusFailed, StatusCanceled,
	}
	seen := make(map[Status]bool)
	for _, s := range statuses {
		if s == "" {
			t.Error("Status constant is empty string")
		}
		if seen[s] {
			t.Errorf("duplicate Status constant: %q", s)
		}
		seen[s] = true
	}
}

// TestPriorityValues verifies priority constants have expected ordering.
func TestPriorityValues(t *testing.T) {
	if PriorityLow >= PriorityNormal {
		t.Error("PriorityLow should be less than PriorityNormal")
	}
	if PriorityNormal >= PriorityHigh {
		t.Error("PriorityNormal should be less than PriorityHigh")
	}
	if PriorityHigh >= PriorityCritical {
		t.Error("PriorityHigh should be less than PriorityCritical")
	}
}

// TestTaskJSONRoundTrip verifies Task serializes and deserializes correctly.
func TestTaskJSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	task := Task{
		ID:          "task-1",
		Title:       "Build feature X",
		Description: "Implement feature X with tests.",
		Status:      StatusInProgress,
		Priority:    PriorityHigh,
		AssignedTo:  "agent-42",
		TeamID:      "team-alpha",
		Labels:      []string{"backend", "urgent"},
		Metadata:    map[string]string{"env": "staging"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	b, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal Task: %v", err)
	}

	var got Task
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal Task: %v", err)
	}

	if got.ID != task.ID {
		t.Errorf("ID: want %q, got %q", task.ID, got.ID)
	}
	if got.Title != task.Title {
		t.Errorf("Title: want %q, got %q", task.Title, got.Title)
	}
	if got.Status != StatusInProgress {
		t.Errorf("Status: want %q, got %q", StatusInProgress, got.Status)
	}
	if got.Priority != PriorityHigh {
		t.Errorf("Priority: want %d, got %d", PriorityHigh, got.Priority)
	}
	if got.AssignedTo != "agent-42" {
		t.Errorf("AssignedTo: want %q, got %q", "agent-42", got.AssignedTo)
	}
	if len(got.Labels) != 2 {
		t.Errorf("Labels: want 2, got %d", len(got.Labels))
	}
	if got.Metadata["env"] != "staging" {
		t.Errorf("Metadata[env]: want staging, got %q", got.Metadata["env"])
	}
}

// TestTaskOptionalFieldsOmitted verifies optional fields are omitted when zero.
func TestTaskOptionalFieldsOmitted(t *testing.T) {
	task := Task{
		ID:        "task-2",
		Title:     "Minimal task",
		Status:    StatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	b, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}

	for _, field := range []string{"assigned_to", "team_id", "parent_id", "result", "error"} {
		if v, ok := raw[field]; ok && v != "" {
			t.Errorf("expected %q to be omitted, got %v", field, v)
		}
	}
	if _, ok := raw["started_at"]; ok {
		t.Error("expected started_at to be omitted when nil")
	}
	if _, ok := raw["completed_at"]; ok {
		t.Error("expected completed_at to be omitted when nil")
	}
}

// TestTaskWithStartedAt verifies StartedAt pointer is serialized when set.
func TestTaskWithStartedAt(t *testing.T) {
	now := time.Now()
	task := Task{
		ID:        "task-3",
		Title:     "In flight",
		Status:    StatusInProgress,
		CreatedAt: now,
		UpdatedAt: now,
		StartedAt: &now,
	}

	b, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := raw["started_at"]; !ok {
		t.Error("expected started_at to be present when set")
	}
}

// TestTaskDependsOn verifies DependsOn slice round-trips correctly.
func TestTaskDependsOn(t *testing.T) {
	task := Task{
		ID:        "task-4",
		Title:     "Dependent task",
		Status:    StatusPending,
		DependsOn: []string{"task-1", "task-2", "task-3"},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	b, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Task
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.DependsOn) != 3 {
		t.Fatalf("DependsOn: want 3 items, got %d", len(got.DependsOn))
	}
	if got.DependsOn[1] != "task-2" {
		t.Errorf("DependsOn[1]: want task-2, got %q", got.DependsOn[1])
	}
}

// TestTaskStatusTransitions verifies common status string values.
func TestTaskStatusTransitions(t *testing.T) {
	transitions := []struct {
		from, to Status
	}{
		{StatusPending, StatusAssigned},
		{StatusAssigned, StatusInProgress},
		{StatusInProgress, StatusCompleted},
		{StatusInProgress, StatusFailed},
	}
	for _, tr := range transitions {
		if tr.from == "" || tr.to == "" {
			t.Errorf("invalid transition: %q -> %q", tr.from, tr.to)
		}
	}
}

// TestTaskWithResult verifies result and error fields round-trip.
func TestTaskWithResult(t *testing.T) {
	task := Task{
		ID:        "task-5",
		Title:     "Completed with result",
		Status:    StatusCompleted,
		Result:    "Output: 42 items processed",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	b, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Task
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Result != task.Result {
		t.Errorf("Result: want %q, got %q", task.Result, got.Result)
	}
}
