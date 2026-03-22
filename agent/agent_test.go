package agent

import (
	"encoding/json"
	"testing"
	"time"
)

// TestStatusValues verifies all Status constants are non-empty and distinct.
func TestStatusValues(t *testing.T) {
	statuses := []Status{StatusIdle, StatusActive, StatusWorking, StatusStopped, StatusError}
	seen := make(map[Status]bool)
	for _, s := range statuses {
		if s == "" {
			t.Errorf("Status constant is empty string")
		}
		if seen[s] {
			t.Errorf("duplicate Status constant: %q", s)
		}
		seen[s] = true
	}
}

// TestPersonalityJSONRoundTrip verifies Personality serializes and deserializes correctly.
func TestPersonalityJSONRoundTrip(t *testing.T) {
	p := Personality{
		Name:         "DevAgent",
		Role:         "developer",
		SystemPrompt: "You are a helpful developer.",
		Model:        "claude-sonnet-4",
	}

	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal Personality: %v", err)
	}

	var got Personality
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal Personality: %v", err)
	}

	if got.Name != p.Name {
		t.Errorf("Name: want %q, got %q", p.Name, got.Name)
	}
	if got.Role != p.Role {
		t.Errorf("Role: want %q, got %q", p.Role, got.Role)
	}
	if got.SystemPrompt != p.SystemPrompt {
		t.Errorf("SystemPrompt: want %q, got %q", p.SystemPrompt, got.SystemPrompt)
	}
	if got.Model != p.Model {
		t.Errorf("Model: want %q, got %q", p.Model, got.Model)
	}
}

// TestPersonalityOmitEmptyModel verifies Model is omitted when empty.
func TestPersonalityOmitEmptyModel(t *testing.T) {
	p := Personality{Name: "Anon", Role: "worker", SystemPrompt: "Do stuff."}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, ok := raw["model"]; ok {
		t.Error("expected model field to be omitted when empty")
	}
}

// TestInfoJSONRoundTrip verifies Info serializes and deserializes correctly.
func TestInfoJSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	info := Info{
		ID:          "agent-1",
		Name:        "TestAgent",
		Personality: &Personality{Name: "TestAgent", Role: "tester", SystemPrompt: "Test."},
		Status:      StatusActive,
		CurrentTask: "task-42",
		StartedAt:   now,
		TeamID:      "team-x",
		IsLead:      true,
	}

	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal Info: %v", err)
	}

	var got Info
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal Info: %v", err)
	}

	if got.ID != info.ID {
		t.Errorf("ID: want %q, got %q", info.ID, got.ID)
	}
	if got.Status != StatusActive {
		t.Errorf("Status: want %q, got %q", StatusActive, got.Status)
	}
	if got.IsLead != info.IsLead {
		t.Errorf("IsLead: want %v, got %v", info.IsLead, got.IsLead)
	}
	if got.Personality == nil {
		t.Fatal("expected Personality to be non-nil after round-trip")
	}
	if got.Personality.Role != "tester" {
		t.Errorf("Personality.Role: want %q, got %q", "tester", got.Personality.Role)
	}
}

// TestInfoNilPersonalityOmitted verifies the personality field is omitted when nil.
func TestInfoNilPersonalityOmitted(t *testing.T) {
	info := Info{ID: "agent-2", Name: "Bare", Status: StatusIdle, StartedAt: time.Now()}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["personality"]; ok {
		t.Error("expected personality field to be omitted when nil")
	}
}

// TestInfoOptionalFieldsOmitted verifies optional string fields omit when empty.
func TestInfoOptionalFieldsOmitted(t *testing.T) {
	info := Info{ID: "agent-3", Name: "Minimal", Status: StatusStopped, StartedAt: time.Now()}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"current_task", "team_id"} {
		if v, ok := raw[field]; ok && v != "" {
			t.Errorf("expected %q to be omitted or empty, got %v", field, v)
		}
	}
}
