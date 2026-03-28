package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// helper: create a temp git repo for testing.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// init a git repo
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	// Create an initial commit so we have a valid repo
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "initial commit")

	return dir
}

// ---------- GitCloneTool ----------

func TestGitCloneTool_Definition(t *testing.T) {
	tool := &GitCloneTool{Workspace: "/tmp"}
	def := tool.Definition()
	if def.Name != "git_clone" {
		t.Errorf("expected name %q, got %q", "git_clone", def.Name)
	}
	if def.Description == "" {
		t.Error("expected non-empty description")
	}
	if def.Parameters == nil {
		t.Error("expected non-nil parameters")
	}
}

func TestGitCloneTool_Execute(t *testing.T) {
	// Create a source repo to clone from
	srcRepo := setupGitRepo(t)
	ws := setupWorkspace(t)
	tool := &GitCloneTool{Workspace: ws}
	ctx := context.Background()

	t.Run("clone local repo", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{
			"repo_url": srcRepo,
			"path":     "cloned",
			"branch":   "master",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("expected map, got %T", result)
		}
		if m["exit_code"] != 0 {
			t.Errorf("expected exit_code 0, got %v (stderr: %s)", m["exit_code"], m["stderr"])
		}
		if m["path"] != "cloned" {
			t.Errorf("expected path %q, got %q", "cloned", m["path"])
		}
		// Verify clone exists
		if _, err := os.Stat(filepath.Join(ws, "cloned", "README.md")); err != nil {
			t.Errorf("expected cloned README.md to exist: %v", err)
		}
	})

	t.Run("missing repo_url", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"path": "dest"})
		if err == nil {
			t.Fatal("expected error for missing repo_url")
		}
	})

	t.Run("missing path", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"repo_url": srcRepo})
		if err == nil {
			t.Fatal("expected error for missing path")
		}
	})
}

// ---------- GitStatusTool ----------

func TestGitStatusTool_Definition(t *testing.T) {
	tool := &GitStatusTool{Workspace: "/tmp"}
	def := tool.Definition()
	if def.Name != "git_status" {
		t.Errorf("expected name %q, got %q", "git_status", def.Name)
	}
}

func TestGitStatusTool_Execute(t *testing.T) {
	repo := setupGitRepo(t)
	tool := &GitStatusTool{Workspace: ""}
	ctx := context.Background()

	t.Run("clean repo", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{"path": repo})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m := result.(map[string]any)
		if m["exit_code"] != 0 {
			t.Errorf("expected exit_code 0, got %v", m["exit_code"])
		}
		stdout := m["stdout"].(string)
		if stdout == "" {
			t.Error("expected non-empty stdout")
		}
	})

	t.Run("missing path", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing path")
		}
	})
}

// ---------- GitCommitTool ----------

func TestGitCommitTool_Definition(t *testing.T) {
	tool := &GitCommitTool{Workspace: "/tmp"}
	def := tool.Definition()
	if def.Name != "git_commit" {
		t.Errorf("expected name %q, got %q", "git_commit", def.Name)
	}
}

func TestGitCommitTool_Execute(t *testing.T) {
	repo := setupGitRepo(t)
	tool := &GitCommitTool{Workspace: ""}
	ctx := context.Background()

	t.Run("commit new file", func(t *testing.T) {
		// Create a new file
		if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new content"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}

		result, err := tool.Execute(ctx, map[string]any{
			"path":    repo,
			"message": "add new file",
			"files":   []any{"new.txt"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m := result.(map[string]any)
		if m["exit_code"] != 0 {
			t.Errorf("expected exit_code 0, got %v (stderr: %s)", m["exit_code"], m["stderr"])
		}
	})

	t.Run("missing path", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"message": "test"})
		if err == nil {
			t.Fatal("expected error for missing path")
		}
	})

	t.Run("missing message", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"path": repo})
		if err == nil {
			t.Fatal("expected error for missing message")
		}
	})
}

// ---------- GitPushTool ----------

func TestGitPushTool_Definition(t *testing.T) {
	tool := &GitPushTool{Workspace: "/tmp"}
	def := tool.Definition()
	if def.Name != "git_push" {
		t.Errorf("expected name %q, got %q", "git_push", def.Name)
	}
}

func TestGitPushTool_Execute(t *testing.T) {
	ctx := context.Background()

	t.Run("missing path", func(t *testing.T) {
		tool := &GitPushTool{Workspace: "/tmp"}
		_, err := tool.Execute(ctx, map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing path")
		}
	})
}

// ---------- GitDiffTool ----------

func TestGitDiffTool_Definition(t *testing.T) {
	tool := &GitDiffTool{Workspace: "/tmp"}
	def := tool.Definition()
	if def.Name != "git_diff" {
		t.Errorf("expected name %q, got %q", "git_diff", def.Name)
	}
}

func TestGitDiffTool_Execute(t *testing.T) {
	repo := setupGitRepo(t)
	tool := &GitDiffTool{Workspace: ""}
	ctx := context.Background()

	t.Run("no changes", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{"path": repo})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m := result.(map[string]any)
		if m["exit_code"] != 0 {
			t.Errorf("expected exit_code 0, got %v", m["exit_code"])
		}
	})

	t.Run("with changes", func(t *testing.T) {
		// Modify a file
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Modified\n"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}

		result, err := tool.Execute(ctx, map[string]any{"path": repo})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m := result.(map[string]any)
		if m["exit_code"] != 0 {
			t.Errorf("expected exit_code 0, got %v", m["exit_code"])
		}
		stdout := m["stdout"].(string)
		if stdout == "" {
			t.Error("expected non-empty diff output")
		}
	})

	t.Run("staged changes", func(t *testing.T) {
		// Stage the change
		cmd := exec.Command("git", "add", "README.md")
		cmd.Dir = repo
		if err := cmd.Run(); err != nil {
			t.Fatalf("git add: %v", err)
		}

		result, err := tool.Execute(ctx, map[string]any{
			"path":   repo,
			"staged": true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m := result.(map[string]any)
		if m["exit_code"] != 0 {
			t.Errorf("expected exit_code 0, got %v", m["exit_code"])
		}
		stdout := m["stdout"].(string)
		if stdout == "" {
			t.Error("expected non-empty staged diff output")
		}
	})

	t.Run("missing path", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing path")
		}
	})
}

// ---------- resolvePath ----------

func TestResolvePath(t *testing.T) {
	tests := []struct {
		workspace string
		path      string
		expected  string
	}{
		{"/ws", "repo", "/ws/repo"},
		{"/ws", "/absolute/path", "/absolute/path"},
		{"", "repo", "repo"},
	}
	for _, tc := range tests {
		got := resolvePath(tc.workspace, tc.path)
		if got != tc.expected {
			t.Errorf("resolvePath(%q, %q) = %q, want %q", tc.workspace, tc.path, got, tc.expected)
		}
	}
}
