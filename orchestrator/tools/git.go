package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// GitCloneTool clones a git repository into the workspace.
type GitCloneTool struct {
	Workspace string
}

func (t *GitCloneTool) Name() string        { return "git_clone" }
func (t *GitCloneTool) Description() string { return "Clone a git repository into the workspace" }
func (t *GitCloneTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo_url": map[string]any{"type": "string", "description": "Git repository URL to clone"},
				"path":     map[string]any{"type": "string", "description": "Destination path within workspace"},
				"branch":   map[string]any{"type": "string", "description": "Branch to clone (default: main)"},
			},
			"required": []string{"repo_url", "path"},
		},
	}
}
func (t *GitCloneTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	repoURL, _ := args["repo_url"].(string)
	if repoURL == "" {
		return nil, fmt.Errorf("repo_url is required")
	}
	path, _ := args["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	branch, _ := args["branch"].(string)
	if branch == "" {
		branch = "main"
	}

	// Inject GITHUB_TOKEN for HTTPS auth if available
	cloneURL := repoURL
	if token := os.Getenv("GITHUB_TOKEN"); token != "" && strings.HasPrefix(repoURL, "https://") {
		// Insert token into URL: https://TOKEN@github.com/...
		cloneURL = strings.Replace(repoURL, "https://", "https://"+token+"@", 1)
	}

	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	cmdArgs := []string{"clone", "--branch", branch, cloneURL, path}
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	if t.Workspace != "" {
		cmd.Dir = t.Workspace
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("git clone: %w", err)
		}
	}

	return map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exitCode,
		"path":      path,
	}, nil
}

// GitStatusTool runs git status in a directory.
type GitStatusTool struct {
	Workspace string
}

func (t *GitStatusTool) Name() string { return "git_status" }
func (t *GitStatusTool) Description() string {
	return "Show the working tree status of a git repository"
}
func (t *GitStatusTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Path to the git repository within workspace"},
			},
			"required": []string{"path"},
		},
	}
}
func (t *GitStatusTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "status")
	cmd.Dir = resolvePath(t.Workspace, path)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("git status: %w", err)
		}
	}

	return map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exitCode,
	}, nil
}

// GitCommitTool stages files and creates a commit.
type GitCommitTool struct {
	Workspace string
}

func (t *GitCommitTool) Name() string        { return "git_commit" }
func (t *GitCommitTool) Description() string { return "Stage files and create a git commit" }
func (t *GitCommitTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Path to the git repository within workspace"},
				"message": map[string]any{"type": "string", "description": "Commit message"},
				"files":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Files to stage (use [\".\"] for all)"},
			},
			"required": []string{"path", "message"},
		},
	}
}
func (t *GitCommitTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	message, _ := args["message"].(string)
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}

	dir := resolvePath(t.Workspace, path)

	// Determine files to stage
	files := []string{"."}
	if filesRaw, ok := args["files"].([]any); ok && len(filesRaw) > 0 {
		files = make([]string, 0, len(filesRaw))
		for _, f := range filesRaw {
			if s, ok := f.(string); ok && s != "" {
				files = append(files, s)
			}
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// git add
	addArgs := append([]string{"add"}, files...)
	addCmd := exec.CommandContext(ctx, "git", addArgs...)
	addCmd.Dir = dir
	var addStderr bytes.Buffer
	addCmd.Stderr = &addStderr
	if err := addCmd.Run(); err != nil {
		return map[string]any{
			"stage":     "add",
			"stderr":    addStderr.String(),
			"exit_code": exitCodeFrom(err),
		}, nil
	}

	// git commit
	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	commitCmd.Dir = dir
	var stdout, stderr bytes.Buffer
	commitCmd.Stdout = &stdout
	commitCmd.Stderr = &stderr
	err := commitCmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = exitCodeFrom(err)
	}

	return map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exitCode,
	}, nil
}

// GitPushTool pushes commits to a remote.
type GitPushTool struct {
	Workspace string
}

func (t *GitPushTool) Name() string        { return "git_push" }
func (t *GitPushTool) Description() string { return "Push commits to a remote git repository" }
func (t *GitPushTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Path to the git repository within workspace"},
				"remote": map[string]any{"type": "string", "description": "Remote name (default: origin)"},
				"branch": map[string]any{"type": "string", "description": "Branch to push (default: current branch)"},
			},
			"required": []string{"path"},
		},
	}
}
func (t *GitPushTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	remote, _ := args["remote"].(string)
	if remote == "" {
		remote = "origin"
	}
	branch, _ := args["branch"].(string)

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	pushArgs := []string{"push", remote}
	if branch != "" {
		pushArgs = append(pushArgs, branch)
	}

	cmd := exec.CommandContext(ctx, "git", pushArgs...)
	cmd.Dir = resolvePath(t.Workspace, path)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("git push: %w", err)
		}
	}

	return map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exitCode,
	}, nil
}

// GitDiffTool shows changes in the working tree.
type GitDiffTool struct {
	Workspace string
}

func (t *GitDiffTool) Name() string        { return "git_diff" }
func (t *GitDiffTool) Description() string { return "Show changes in the working tree or staging area" }
func (t *GitDiffTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Path to the git repository within workspace"},
				"staged": map[string]any{"type": "boolean", "description": "Show staged changes (default: false)"},
			},
			"required": []string{"path"},
		},
	}
}
func (t *GitDiffTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	staged, _ := args["staged"].(bool)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	diffArgs := []string{"diff"}
	if staged {
		diffArgs = append(diffArgs, "--staged")
	}

	cmd := exec.CommandContext(ctx, "git", diffArgs...)
	cmd.Dir = resolvePath(t.Workspace, path)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("git diff: %w", err)
		}
	}

	return map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exitCode,
	}, nil
}

// resolvePath resolves a relative path within a workspace directory.
func resolvePath(workspace, path string) string {
	if workspace == "" {
		return path
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	return workspace + "/" + path
}

// exitCodeFrom extracts exit code from an error.
func exitCodeFrom(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 1
}
