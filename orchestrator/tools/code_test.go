package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCodeReviewTool_Definition(t *testing.T) {
	tool := &CodeReviewTool{}
	if tool.Name() != "code_review" {
		t.Fatalf("expected name code_review, got %s", tool.Name())
	}
	def := tool.Definition()
	if def.Name != "code_review" {
		t.Fatalf("expected def name code_review, got %s", def.Name)
	}
	params, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}
	if _, ok := params["path"]; !ok {
		t.Fatal("expected 'path' parameter")
	}
}

func TestCodeReviewTool_Execute(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

func main() {
	x := 1
	_ = x
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\ngo 1.22\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	tool := &CodeReviewTool{}
	result, err := tool.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if _, ok := m["findings"]; !ok {
		t.Fatal("expected 'findings' key in result")
	}
	if _, ok := m["count"]; !ok {
		t.Fatal("expected 'count' key in result")
	}
	if _, ok := m["passed"]; !ok {
		t.Fatal("expected 'passed' key in result")
	}
}

func TestCodeReviewTool_Execute_MissingPath(t *testing.T) {
	tool := &CodeReviewTool{}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestCodeReviewTool_Execute_InvalidPath(t *testing.T) {
	tool := &CodeReviewTool{}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "/nonexistent/path/xyz"})
	if err != nil {
		return
	}
	if m, ok := result.(map[string]any); ok && m["error"] != nil {
		return
	}
}

func TestCodeComplexityTool_Definition(t *testing.T) {
	tool := &CodeComplexityTool{}
	if tool.Name() != "code_complexity" {
		t.Fatalf("expected name code_complexity, got %s", tool.Name())
	}
	def := tool.Definition()
	params, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}
	if _, ok := params["path"]; !ok {
		t.Fatal("expected 'path' parameter")
	}
}

func TestCodeComplexityTool_Execute(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

// TODO: refactor this function
func main() {
	x := 1
	_ = x
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	tool := &CodeComplexityTool{}
	result, err := tool.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	todos, ok := m["todos"].([]map[string]any)
	if !ok {
		t.Fatal("expected todos slice")
	}
	if len(todos) == 0 {
		t.Fatal("expected at least one TODO marker")
	}
	if todos[0]["pattern"] != "TODO" {
		t.Fatalf("expected TODO pattern, got %v", todos[0]["pattern"])
	}
}

func TestCodeComplexityTool_Execute_MissingPath(t *testing.T) {
	tool := &CodeComplexityTool{}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestCodeDiffReviewTool_Definition(t *testing.T) {
	tool := &CodeDiffReviewTool{}
	if tool.Name() != "code_diff_review" {
		t.Fatalf("expected name code_diff_review, got %s", tool.Name())
	}
}

func TestCodeDiffReviewTool_Execute(t *testing.T) {
	// setupGitRepo is defined in git_test.go — creates a repo with one initial commit.
	dir := setupGitRepo(t)

	// Create a feature branch and add a new file.
	gitRun(t, dir, "checkout", "-b", "feature")
	err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("hello\nworld\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "feature.txt")
	gitRun(t, dir, "commit", "-m", "add feature file")

	tool := &CodeDiffReviewTool{}
	result, err := tool.Execute(context.Background(), map[string]any{
		"repo_path": dir,
		"base_ref":  "HEAD~1",
		"head_ref":  "HEAD",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if m["file_count"].(int) != 1 {
		t.Fatalf("expected 1 file changed, got %v", m["file_count"])
	}
}

func TestCodeDiffReviewTool_Execute_MissingArgs(t *testing.T) {
	tool := &CodeDiffReviewTool{}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing repo_path")
	}
	_, err = tool.Execute(context.Background(), map[string]any{"repo_path": "/tmp"})
	if err == nil {
		t.Fatal("expected error for missing base_ref")
	}
}

// gitRun runs a git command in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %s: %v", args, out, err)
	}
}

// ---------- GitLogStatsTool ----------

func TestGitLogStatsTool_Name(t *testing.T) {
	tool := &GitLogStatsTool{}
	if tool.Name() != "git_log_stats" {
		t.Fatalf("expected name git_log_stats, got %s", tool.Name())
	}
}

func TestGitLogStatsTool_Execute(t *testing.T) {
	// Use the existing setupGitRepo helper which creates a repo with one commit.
	dir := setupGitRepo(t)

	// Configure git identity for subsequent commits in this test.
	gitRun(t, dir, "config", "user.email", "test@test.com")
	gitRun(t, dir, "config", "user.name", "TestAuthor")

	// Add a second file and commit so there is meaningful log history.
	err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)
	if err != nil {
		t.Fatalf("write file: %v", err)
	}
	gitRun(t, dir, "add", "main.go")
	// Set author env so the author name is deterministic.
	cmd := exec.Command("git", "commit", "-m", "add main.go")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=TestAuthor",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=TestAuthor",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	tool := &GitLogStatsTool{}
	result, execErr := tool.Execute(context.Background(), map[string]any{
		"repo_path": dir,
		"days":      float64(365),
		"limit":     float64(10),
	})
	if execErr != nil {
		t.Fatalf("unexpected error: %v", execErr)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	// Verify top-level keys exist.
	if _, ok := m["total_commits"]; !ok {
		t.Error("expected 'total_commits' key in result")
	}
	if _, ok := m["hotspots"]; !ok {
		t.Error("expected 'hotspots' key in result")
	}
	if _, ok := m["contributors"]; !ok {
		t.Error("expected 'contributors' key in result")
	}

	totalCommits, ok := m["total_commits"].(int)
	if !ok {
		t.Fatalf("expected total_commits to be int, got %T", m["total_commits"])
	}
	if totalCommits < 1 {
		t.Errorf("expected at least 1 commit, got %d", totalCommits)
	}

	// Verify hotspots have the expected structure.
	hotspots, ok := m["hotspots"].([]map[string]any)
	if !ok {
		t.Fatalf("expected hotspots to be []map[string]any, got %T", m["hotspots"])
	}
	if len(hotspots) == 0 {
		t.Error("expected at least one hotspot entry")
	}
	for _, h := range hotspots {
		if _, ok := h["file"]; !ok {
			t.Error("hotspot entry missing 'file' key")
		}
		if _, ok := h["changes"]; !ok {
			t.Error("hotspot entry missing 'changes' key")
		}
	}

	// Verify contributors have the expected structure.
	contributors, ok := m["contributors"].([]map[string]any)
	if !ok {
		t.Fatalf("expected contributors to be []map[string]any, got %T", m["contributors"])
	}
	if len(contributors) == 0 {
		t.Error("expected at least one contributor entry")
	}
	for _, c := range contributors {
		if _, ok := c["name"]; !ok {
			t.Error("contributor entry missing 'name' key")
		}
		if _, ok := c["commits"]; !ok {
			t.Error("contributor entry missing 'commits' key")
		}
	}
}

func TestGitLogStatsTool_Execute_MissingPath(t *testing.T) {
	tool := &GitLogStatsTool{}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing repo_path")
	}
}

func TestGitLogStatsTool_Execute_NotARepo(t *testing.T) {
	// A plain temp dir that is not a git repo.
	dir := t.TempDir()

	tool := &GitLogStatsTool{}
	result, err := tool.Execute(context.Background(), map[string]any{
		"repo_path": dir,
	})
	// The tool swallows git errors and returns empty slices, so err may be nil.
	if err != nil {
		// An error return is also acceptable.
		return
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	// total_commits should be 0 for a non-repo directory.
	totalCommits, _ := m["total_commits"].(int)
	if totalCommits != 0 {
		t.Errorf("expected 0 commits for non-repo dir, got %d", totalCommits)
	}
}

// ---------- TestCoverageTool ----------

func TestTestCoverageTool_Name(t *testing.T) {
	tool := &TestCoverageTool{}
	if tool.Name() != "test_coverage" {
		t.Fatalf("expected name test_coverage, got %s", tool.Name())
	}
}

func TestTestCoverageTool_Execute_MissingPath(t *testing.T) {
	tool := &TestCoverageTool{}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}
