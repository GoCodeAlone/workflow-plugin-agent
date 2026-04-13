// Package safety implements static analysis for shell command safety evaluation.
package safety

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// PolicyMode determines how commands are evaluated.
type PolicyMode string

const (
	ModeAllowlist PolicyMode = "allowlist"
	ModeBlocklist PolicyMode = "blocklist"
	ModeDisabled  PolicyMode = "disabled"
)

// Policy configures the command analyzer.
type Policy struct {
	Mode                 PolicyMode `yaml:"mode" json:"mode"`
	AllowedCommands      []string   `yaml:"allowed_commands,omitempty" json:"allowed_commands,omitempty"`
	BlockedPatterns      []string   `yaml:"blocked_patterns,omitempty" json:"blocked_patterns,omitempty"`
	BlockPipeToShell     bool       `yaml:"block_pipe_to_shell" json:"block_pipe_to_shell"`
	BlockScriptExec      bool       `yaml:"block_script_execution" json:"block_script_execution"`
	EnableStaticAnalysis bool       `yaml:"enable_static_analysis" json:"enable_static_analysis"`
	MaxCommandLength     int        `yaml:"max_command_length" json:"max_command_length"`
}

// DefaultPolicy returns a secure default policy.
func DefaultPolicy() Policy {
	return Policy{
		Mode:                 ModeBlocklist,
		BlockPipeToShell:     true,
		BlockScriptExec:      true,
		EnableStaticAnalysis: true,
		MaxCommandLength:     4096,
		BlockedPatterns: []string{
			"rm -rf /", "rm -rf *", "rm -rf .",
			"mkfs", "dd if=", "chmod 777",
			":(){ :|:& };:",
		},
	}
}

// Risk describes a detected security risk in a command.
type Risk struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Command     string `json:"command,omitempty"`
}

// CommandVerdict is the analysis result for a command.
type CommandVerdict struct {
	Safe   bool   `json:"safe"`
	Reason string `json:"reason,omitempty"`
	Risks  []Risk `json:"risks,omitempty"`
}

// CommandAnalyzer performs static analysis on shell commands.
type CommandAnalyzer struct {
	policy Policy
}

// NewCommandAnalyzer creates an analyzer with the given policy.
func NewCommandAnalyzer(policy Policy) *CommandAnalyzer {
	return &CommandAnalyzer{policy: policy}
}

// Analyze parses and evaluates a command for safety.
func (a *CommandAnalyzer) Analyze(cmd string) (*CommandVerdict, error) {
	if a.policy.Mode == ModeDisabled {
		return &CommandVerdict{Safe: true}, nil
	}

	if a.policy.MaxCommandLength > 0 && len(cmd) > a.policy.MaxCommandLength {
		return &CommandVerdict{
			Safe:   false,
			Reason: fmt.Sprintf("command exceeds max length (%d > %d)", len(cmd), a.policy.MaxCommandLength),
		}, nil
	}

	v := &CommandVerdict{Safe: true}

	// Check raw command string against blocked patterns before parsing.
	for _, pattern := range a.policy.BlockedPatterns {
		if strings.Contains(cmd, pattern) {
			v.Risks = append(v.Risks, Risk{
				Type:        "destructive",
				Description: fmt.Sprintf("matches blocked pattern %q", pattern),
				Command:     cmd,
			})
		}
	}

	// Parse shell AST
	parser := syntax.NewParser()
	prog, err := parser.Parse(strings.NewReader(cmd), "")
	if err != nil {
		return &CommandVerdict{Safe: false, Reason: fmt.Sprintf("failed to parse: %v", err)}, nil
	}

	// Walk AST and collect all command names and check for risks.
	var commands []string
	syntax.Walk(prog, func(node syntax.Node) bool {
		if call, ok := node.(*syntax.CallExpr); ok && len(call.Args) > 0 {
			cmdName := extractCommandName(call)
			commands = append(commands, cmdName)
			fullCmd := nodeToString(call)
			a.checkDestructive(v, fullCmd, cmdName)
		}
		// Check pipe-to-shell
		if binaryCmd, ok := node.(*syntax.BinaryCmd); ok {
			if binaryCmd.Op == syntax.Pipe {
				a.checkPipeToShell(v, binaryCmd)
			}
		}
		return true
	})

	// Allowlist mode: only allowed commands pass.
	if a.policy.Mode == ModeAllowlist && len(commands) > 0 {
		for _, c := range commands {
			if !a.isAllowed(c) {
				v.Risks = append(v.Risks, Risk{
					Type:        "not_allowed",
					Description: fmt.Sprintf("command %q is not in the allowlist", c),
					Command:     c,
				})
			}
		}
	}

	// Static analysis checks.
	if a.policy.EnableStaticAnalysis {
		a.checkEncoded(v, cmd)
		a.checkScriptExecution(v, cmd, prog)
	}

	if len(v.Risks) > 0 {
		v.Safe = false
		if v.Reason == "" {
			v.Reason = v.Risks[0].Description
		}
	}

	return v, nil
}

func (a *CommandAnalyzer) checkDestructive(v *CommandVerdict, fullCmd, cmdName string) {
	for _, pattern := range a.policy.BlockedPatterns {
		if strings.Contains(fullCmd, pattern) {
			v.Risks = append(v.Risks, Risk{
				Type:        "destructive",
				Description: fmt.Sprintf("matches blocked pattern %q", pattern),
				Command:     fullCmd,
			})
			return
		}
	}
	// Always block well-known disk-wiping commands regardless of policy patterns.
	alwaysDestructive := map[string]bool{"mkfs": true, "fdisk": true, "wipefs": true}
	if alwaysDestructive[cmdName] {
		v.Risks = append(v.Risks, Risk{
			Type:        "destructive",
			Description: fmt.Sprintf("%q is a destructive command", cmdName),
			Command:     fullCmd,
		})
	}
}

func (a *CommandAnalyzer) checkPipeToShell(v *CommandVerdict, bc *syntax.BinaryCmd) {
	if !a.policy.BlockPipeToShell {
		return
	}
	shells := map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true}
	if call, ok := bc.Y.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 {
		name := extractCommandName(call)
		if shells[name] {
			v.Risks = append(v.Risks, Risk{
				Type:        "pipe_to_shell",
				Description: fmt.Sprintf("pipes output to %s", name),
			})
		}
	}
}

func (a *CommandAnalyzer) checkEncoded(v *CommandVerdict, cmd string) {
	if strings.Contains(cmd, "base64") &&
		(strings.Contains(cmd, "| sh") || strings.Contains(cmd, "| bash")) {
		v.Risks = append(v.Risks, Risk{
			Type:        "encoded_command",
			Description: "base64 decode piped to shell",
		})
	}
}

func (a *CommandAnalyzer) checkScriptExecution(v *CommandVerdict, cmd string, _ *syntax.File) {
	if !a.policy.BlockScriptExec {
		return
	}
	// python/python3 inline code with shell execution.
	if (strings.Contains(cmd, "python -c") || strings.Contains(cmd, "python3 -c")) &&
		(strings.Contains(cmd, "os.system") || strings.Contains(cmd, "subprocess")) {
		v.Risks = append(v.Risks, Risk{
			Type:        "script_execution",
			Description: "python inline code with shell execution",
		})
	}
	// Write-then-execute patterns.
	scriptExtensions := []string{".sh", ".bash", ".py", ".rb", ".pl"}
	for _, ext := range scriptExtensions {
		if strings.Contains(cmd, "> ") && strings.Contains(cmd, ext) &&
			(strings.Contains(cmd, "&& bash") || strings.Contains(cmd, "&& sh") ||
				strings.Contains(cmd, "&& chmod") || strings.Contains(cmd, "&& ./")) {
			v.Risks = append(v.Risks, Risk{
				Type:        "script_execution",
				Description: fmt.Sprintf("writes and executes a %s script", ext),
			})
		}
	}
}

func (a *CommandAnalyzer) isAllowed(cmd string) bool {
	for _, allowed := range a.policy.AllowedCommands {
		if cmd == allowed || strings.HasPrefix(cmd, allowed) {
			return true
		}
	}
	return false
}

func extractCommandName(call *syntax.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	parts := call.Args[0].Parts
	if len(parts) == 0 {
		return ""
	}
	if lit, ok := parts[0].(*syntax.Lit); ok {
		return lit.Value
	}
	return ""
}

func nodeToString(node syntax.Node) string {
	var buf strings.Builder
	syntax.NewPrinter().Print(&buf, node)
	return buf.String()
}
