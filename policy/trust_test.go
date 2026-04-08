package policy

import (
	"context"
	"testing"
)

func TestActionConstants(t *testing.T) {
	if Allow != "allow" || Deny != "deny" || Ask != "ask" {
		t.Fatal("action constants changed")
	}
}

func TestModePresetsExist(t *testing.T) {
	for _, mode := range []string{"conservative", "permissive", "locked", "sandbox"} {
		if _, ok := ModePresets[mode]; !ok {
			t.Errorf("missing mode preset %q", mode)
		}
	}
}

func TestNewTrustEngine(t *testing.T) {
	te := NewTrustEngine("conservative", nil, nil)
	if te == nil {
		t.Fatal("NewTrustEngine returned nil")
	}
	if te.Mode() != "conservative" {
		t.Errorf("mode = %q, want conservative", te.Mode())
	}
}

func TestSetMode(t *testing.T) {
	te := NewTrustEngine("conservative", nil, nil)
	changed := te.SetMode("permissive")
	if te.Mode() != "permissive" {
		t.Errorf("mode = %q, want permissive", te.Mode())
	}
	if len(changed) == 0 {
		t.Error("SetMode returned empty rules")
	}
}

func TestSetModeUnknown(t *testing.T) {
	te := NewTrustEngine("conservative", nil, nil)
	changed := te.SetMode("nonexistent")
	if len(changed) != 0 {
		t.Error("SetMode for unknown mode should return nil")
	}
	if te.Mode() != "conservative" {
		t.Error("mode should not change for unknown preset")
	}
}

func TestEvaluateToolAllow(t *testing.T) {
	rules := []TrustRule{
		{Pattern: "file_read", Action: Allow},
	}
	te := NewTrustEngine("custom", rules, nil)
	action := te.Evaluate(context.Background(), "file_read", nil)
	if action != Allow {
		t.Errorf("got %v, want Allow", action)
	}
}

func TestEvaluateToolDenyWins(t *testing.T) {
	rules := []TrustRule{
		{Pattern: "*", Action: Allow},
		{Pattern: "bash:rm -rf *", Action: Deny},
	}
	te := NewTrustEngine("custom", rules, nil)
	action := te.EvaluateCommand("rm -rf /")
	if action != Deny {
		t.Errorf("got %v, want Deny", action)
	}
}

func TestEvaluateToolDefaultDeny(t *testing.T) {
	te := NewTrustEngine("custom", nil, nil)
	action := te.Evaluate(context.Background(), "unknown_tool", nil)
	if action != Deny {
		t.Errorf("got %v, want Deny", action)
	}
}

func TestEvaluateWildcardPrefix(t *testing.T) {
	rules := []TrustRule{
		{Pattern: "blackboard_*", Action: Allow},
	}
	te := NewTrustEngine("custom", rules, nil)
	if te.Evaluate(context.Background(), "blackboard_read", nil) != Allow {
		t.Error("wildcard prefix should match")
	}
	if te.Evaluate(context.Background(), "file_read", nil) != Deny {
		t.Error("non-matching tool should deny")
	}
}

func TestEvaluateCommandBashPrefix(t *testing.T) {
	rules := []TrustRule{
		{Pattern: "bash:git *", Action: Allow},
		{Pattern: "bash:go *", Action: Allow},
	}
	te := NewTrustEngine("custom", rules, nil)
	if te.EvaluateCommand("git status") != Allow {
		t.Error("git command should be allowed")
	}
	if te.EvaluateCommand("go test ./...") != Allow {
		t.Error("go command should be allowed")
	}
	if te.EvaluateCommand("rm -rf /") != Deny {
		t.Error("rm command should be denied")
	}
}

func TestEvaluatePathRule(t *testing.T) {
	rules := []TrustRule{
		{Pattern: "path:/tmp/*", Action: Allow},
		{Pattern: "path:~/.ssh/*", Action: Deny},
	}
	te := NewTrustEngine("custom", rules, nil)
	if te.EvaluatePath("/tmp/foo.txt") != Allow {
		t.Error("/tmp should be allowed")
	}
}

func TestRulesFromScope(t *testing.T) {
	rules := []TrustRule{
		{Pattern: "file_read", Action: Allow, Scope: "global"},
		{Pattern: "file_write", Action: Deny, Scope: "agent:coder"},
		{Pattern: "file_write", Action: Allow, Scope: "agent:reviewer"},
	}
	te := NewTrustEngine("custom", rules, nil)
	// With agent scope
	action := te.EvaluateScoped(context.Background(), "file_write", nil, "agent:coder")
	if action != Deny {
		t.Errorf("coder scope: got %v, want Deny", action)
	}
	action = te.EvaluateScoped(context.Background(), "file_write", nil, "agent:reviewer")
	if action != Allow {
		t.Errorf("reviewer scope: got %v, want Allow", action)
	}
}

// Task 1.2: Claude Code format parser tests

func TestParseClaudeCodeSettings(t *testing.T) {
	settings := `{
		"allowedTools": ["Edit", "Read", "Bash(git:*)", "Bash(go:*)"],
		"disallowedTools": ["Bash(rm -rf:*)", "Bash(sudo:*)"]
	}`
	rules, err := ParseClaudeCodeSettings([]byte(settings))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 6 {
		t.Fatalf("expected 6 rules, got %d", len(rules))
	}
	// Check allowed
	found := false
	for _, r := range rules {
		if r.Pattern == "Edit" && r.Action == Allow {
			found = true
		}
	}
	if !found {
		t.Error("expected Edit allow rule")
	}
	// Check bash conversion
	for _, r := range rules {
		if r.Pattern == "bash:git *" && r.Action == Allow {
			found = true
		}
	}
	if !found {
		t.Error("expected bash:git * allow rule")
	}
	// Check deny
	for _, r := range rules {
		if r.Pattern == "bash:rm -rf *" && r.Action == Deny {
			found = true
		}
	}
	if !found {
		t.Error("expected bash:rm -rf * deny rule")
	}
}

func TestParseClaudeCodeBashFormat(t *testing.T) {
	tests := []struct {
		input   string
		pattern string
	}{
		{"Bash(git:*)", "bash:git *"},
		{"Bash(go:*)", "bash:go *"},
		{"Bash(rm -rf:*)", "bash:rm -rf *"},
		{"Edit", "Edit"},
		{"Read", "Read"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeClaudeToolPattern(tt.input)
			if got != tt.pattern {
				t.Errorf("normalizeClaudeToolPattern(%q) = %q, want %q", tt.input, got, tt.pattern)
			}
		})
	}
}

func TestParseClaudeCodeSettingsEmpty(t *testing.T) {
	rules, err := ParseClaudeCodeSettings([]byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 0 {
		t.Errorf("expected 0 rules, got %d", len(rules))
	}
}

// Task 1.3: Ratchet YAML format parser tests

func TestParseRatchetTrustConfig(t *testing.T) {
	cfg := RatchetTrustConfig{
		Mode: "conservative",
		Rules: []RatchetTrustRule{
			{Pattern: "file_read", Action: "allow"},
			{Pattern: "bash:git *", Action: "allow"},
			{Pattern: "bash:rm -rf *", Action: "deny"},
			{Pattern: "bash:docker *", Action: "ask"},
			{Pattern: "path:/tmp/*", Action: "allow"},
			{Pattern: "path:~/.ssh/*", Action: "deny"},
		},
	}
	rules := ParseRatchetTrustConfig(cfg)
	if len(rules) != 6 {
		t.Fatalf("expected 6 rules, got %d", len(rules))
	}
	if rules[0].Pattern != "file_read" || rules[0].Action != Allow {
		t.Errorf("rule 0: %+v", rules[0])
	}
	if rules[2].Action != Deny {
		t.Errorf("rule 2: expected Deny, got %v", rules[2].Action)
	}
	if rules[3].Action != Ask {
		t.Errorf("rule 3: expected Ask, got %v", rules[3].Action)
	}
}

func TestParseRatchetTrustConfigEmpty(t *testing.T) {
	rules := ParseRatchetTrustConfig(RatchetTrustConfig{})
	if len(rules) != 0 {
		t.Errorf("expected 0 rules, got %d", len(rules))
	}
}
