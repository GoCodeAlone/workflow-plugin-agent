package orchestrator

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestSkillDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSkillManager_InitTables(t *testing.T) {
	db := newTestSkillDB(t)
	sm := NewSkillManager(db, "")

	if err := sm.InitTables(); err != nil {
		t.Fatalf("InitTables: %v", err)
	}

	// Verify tables exist by querying them
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM skills").Scan(&count); err != nil {
		t.Errorf("skills table missing: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM agent_skills").Scan(&count); err != nil {
		t.Errorf("agent_skills table missing: %v", err)
	}
}

func TestSkillManager_LoadFromDirectory(t *testing.T) {
	db := newTestSkillDB(t)
	sm := NewSkillManager(db, "")
	if err := sm.InitTables(); err != nil {
		t.Fatalf("InitTables: %v", err)
	}

	// Create a temporary skill directory
	dir := t.TempDir()
	skillContent := `---
name: Test Skill
description: A skill for testing
category: test
required_tools: [file_read, shell_exec]
---
## Test Skill Content

This is test skill content.
It has multiple lines.
`
	if err := os.WriteFile(filepath.Join(dir, "test-skill.md"), []byte(skillContent), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	// Write a non-md file that should be ignored
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("ignored"), 0644); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}

	sm.skillDir = dir
	if err := sm.LoadFromDirectory(); err != nil {
		t.Fatalf("LoadFromDirectory: %v", err)
	}

	skills, err := sm.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}

	s := skills[0]
	if s.ID != "test-skill" {
		t.Errorf("expected ID test-skill, got %q", s.ID)
	}
	if s.Name != "Test Skill" {
		t.Errorf("expected name 'Test Skill', got %q", s.Name)
	}
	if s.Description != "A skill for testing" {
		t.Errorf("expected description 'A skill for testing', got %q", s.Description)
	}
	if s.Category != "test" {
		t.Errorf("expected category 'test', got %q", s.Category)
	}
	if len(s.RequiredTools) != 2 {
		t.Errorf("expected 2 required tools, got %d", len(s.RequiredTools))
	}
	if s.Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestSkillManager_LoadFromDirectory_NonExistent(t *testing.T) {
	db := newTestSkillDB(t)
	sm := NewSkillManager(db, "/nonexistent/path/that/does/not/exist")
	if err := sm.InitTables(); err != nil {
		t.Fatalf("InitTables: %v", err)
	}
	// Should not error on missing directory
	if err := sm.LoadFromDirectory(); err != nil {
		t.Errorf("LoadFromDirectory on nonexistent dir should not error: %v", err)
	}
}

func TestSkillManager_AssignToAgent(t *testing.T) {
	db := newTestSkillDB(t)
	sm := NewSkillManager(db, "")
	if err := sm.InitTables(); err != nil {
		t.Fatalf("InitTables: %v", err)
	}

	ctx := context.Background()

	// Insert a skill directly
	_, err := db.Exec("INSERT INTO skills (id, name, description, content, category, required_tools) VALUES ('skill-1', 'Skill One', 'desc', 'content', 'dev', '[]')")
	if err != nil {
		t.Fatalf("insert skill: %v", err)
	}

	// Assign skill to agent
	if err := sm.AssignToAgent(ctx, "agent-1", "skill-1"); err != nil {
		t.Fatalf("AssignToAgent: %v", err)
	}

	// Assigning again should be idempotent (INSERT OR IGNORE)
	if err := sm.AssignToAgent(ctx, "agent-1", "skill-1"); err != nil {
		t.Errorf("AssignToAgent duplicate should not error: %v", err)
	}

	skills, err := sm.GetAgentSkills(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Errorf("expected 1 skill, got %d", len(skills))
	}
}

func TestSkillManager_RemoveFromAgent(t *testing.T) {
	db := newTestSkillDB(t)
	sm := NewSkillManager(db, "")
	if err := sm.InitTables(); err != nil {
		t.Fatalf("InitTables: %v", err)
	}

	ctx := context.Background()

	_, _ = db.Exec("INSERT INTO skills (id, name, description, content, category, required_tools) VALUES ('skill-1', 'Skill One', 'desc', 'content', 'dev', '[]')")
	_ = sm.AssignToAgent(ctx, "agent-1", "skill-1")

	if err := sm.RemoveFromAgent(ctx, "agent-1", "skill-1"); err != nil {
		t.Fatalf("RemoveFromAgent: %v", err)
	}

	skills, err := sm.GetAgentSkills(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentSkills: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills after removal, got %d", len(skills))
	}
}

func TestSkillManager_GetAgentSkills(t *testing.T) {
	db := newTestSkillDB(t)
	sm := NewSkillManager(db, "")
	if err := sm.InitTables(); err != nil {
		t.Fatalf("InitTables: %v", err)
	}

	ctx := context.Background()

	// Insert two skills
	for _, row := range []struct{ id, name string }{
		{"skill-a", "Skill A"},
		{"skill-b", "Skill B"},
	} {
		_, err := db.Exec("INSERT INTO skills (id, name, description, content, category, required_tools) VALUES (?, ?, '', 'content', 'dev', '[]')", row.id, row.name)
		if err != nil {
			t.Fatalf("insert skill %s: %v", row.id, err)
		}
	}

	_ = sm.AssignToAgent(ctx, "agent-1", "skill-a")
	_ = sm.AssignToAgent(ctx, "agent-1", "skill-b")
	_ = sm.AssignToAgent(ctx, "agent-2", "skill-a")

	agent1Skills, err := sm.GetAgentSkills(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentSkills agent-1: %v", err)
	}
	if len(agent1Skills) != 2 {
		t.Errorf("agent-1: expected 2 skills, got %d", len(agent1Skills))
	}

	agent2Skills, err := sm.GetAgentSkills(ctx, "agent-2")
	if err != nil {
		t.Fatalf("GetAgentSkills agent-2: %v", err)
	}
	if len(agent2Skills) != 1 {
		t.Errorf("agent-2: expected 1 skill, got %d", len(agent2Skills))
	}
}

func TestSkillManager_BuildSkillPrompt(t *testing.T) {
	db := newTestSkillDB(t)
	sm := NewSkillManager(db, "")
	if err := sm.InitTables(); err != nil {
		t.Fatalf("InitTables: %v", err)
	}

	ctx := context.Background()

	// No skills assigned — should return empty string
	prompt, err := sm.BuildSkillPrompt(ctx, "agent-1")
	if err != nil {
		t.Fatalf("BuildSkillPrompt (no skills): %v", err)
	}
	if prompt != "" {
		t.Errorf("expected empty prompt for agent with no skills, got %q", prompt)
	}

	// Insert and assign a skill
	_, _ = db.Exec("INSERT INTO skills (id, name, description, content, category, required_tools) VALUES ('code-review', 'Code Review', 'Review methodology', '## Review Steps\n1. Check logic\n2. Check security', 'development', '[]')")
	_ = sm.AssignToAgent(ctx, "agent-1", "code-review")

	prompt, err = sm.BuildSkillPrompt(ctx, "agent-1")
	if err != nil {
		t.Fatalf("BuildSkillPrompt: %v", err)
	}
	if prompt == "" {
		t.Error("expected non-empty prompt after assigning skill")
	}
	if len(prompt) < 10 {
		t.Errorf("prompt seems too short: %q", prompt)
	}
	// Should contain the skill name and content
	if !contains(prompt, "Code Review") {
		t.Error("prompt missing skill name 'Code Review'")
	}
	if !contains(prompt, "## Review Steps") {
		t.Error("prompt missing skill content")
	}
}

func TestParseSkillFile(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  string
		wantID   string
		wantName string
		wantCat  string
	}{
		{
			name:     "with frontmatter",
			filename: "my-skill.md",
			content: `---
name: My Skill
description: Does things
category: analysis
required_tools: [tool_a]
---
## Body Content`,
			wantID:   "my-skill",
			wantName: "My Skill",
			wantCat:  "analysis",
		},
		{
			name:     "no frontmatter",
			filename: "bare-skill.md",
			content:  "## Just Content",
			wantID:   "bare-skill",
			wantName: "Bare Skill",
			wantCat:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			skill, err := parseSkillFile(tc.filename, []byte(tc.content))
			if err != nil {
				t.Fatalf("parseSkillFile: %v", err)
			}
			if skill.ID != tc.wantID {
				t.Errorf("ID: got %q, want %q", skill.ID, tc.wantID)
			}
			if skill.Name != tc.wantName {
				t.Errorf("Name: got %q, want %q", skill.Name, tc.wantName)
			}
			if skill.Category != tc.wantCat {
				t.Errorf("Category: got %q, want %q", skill.Category, tc.wantCat)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && len(substr) > 0 &&
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
