package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// WorkspaceInitStep creates a project workspace directory.
type WorkspaceInitStep struct {
	name          string
	dataDir       string
	projectIDExpr string // template expression for project ID (e.g. "{{ .steps.prepare.id }}")
	app           modular.Application
	tmpl          *module.TemplateEngine
}

func (s *WorkspaceInitStep) Name() string { return s.name }

func (s *WorkspaceInitStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	var projectID string

	// Try template expression first (from config)
	if s.projectIDExpr != "" {
		if resolved, err := s.tmpl.Resolve(s.projectIDExpr, pc); err == nil {
			projectID = fmt.Sprintf("%v", resolved)
		}
	}

	// Fall back to pc.Current fields
	if projectID == "" {
		projectID = extractString(pc.Current, "project_id", "")
	}
	if projectID == "" {
		projectID = extractString(pc.Current, "id", "")
	}
	if projectID == "" {
		return nil, fmt.Errorf("workspace_init step %q: project_id is required", s.name)
	}

	wsPath := filepath.Join(s.dataDir, "workspaces", projectID)

	// Create standard subdirectories
	for _, sub := range []string{"src", "output", "logs"} {
		dir := filepath.Join(wsPath, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("workspace_init step %q: create %s: %w", s.name, sub, err)
		}
	}

	// Update project workspace_path in DB if we have a DB provider
	if svc, ok := s.app.SvcRegistry()["ratchet-db"]; ok {
		if dbp, ok := svc.(module.DBProvider); ok && dbp.DB() != nil {
			db := dbp.DB()
			_, _ = db.ExecContext(ctx,
				"UPDATE projects SET workspace_path = ?, updated_at = datetime('now') WHERE id = ?",
				wsPath, projectID,
			)

			// Clone pending repos for this project
			s.clonePendingRepos(ctx, projectID, wsPath)
		}
	}

	return &module.StepResult{
		Output: map[string]any{
			"workspace_path": wsPath,
			"project_id":     projectID,
		},
	}, nil
}

func newWorkspaceInitFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		dataDir, _ := cfg["data_dir"].(string)
		if dataDir == "" {
			dataDir = "./data"
		}
		projectIDExpr, _ := cfg["project_id"].(string)
		return &WorkspaceInitStep{
			name:          name,
			dataDir:       dataDir,
			projectIDExpr: projectIDExpr,
			app:           app,
			tmpl:          module.NewTemplateEngine(),
		}, nil
	}
}

// clonePendingRepos queries project_repos for pending repos and clones them into workspace/src/.
func (s *WorkspaceInitStep) clonePendingRepos(ctx context.Context, projectID, wsPath string) {
	type repoRow struct {
		ID      string
		RepoURL string
		Branch  string
	}

	// Use the raw *sql.DB for query
	svc, ok := s.app.SvcRegistry()["ratchet-db"]
	if !ok {
		return
	}
	dbp, ok := svc.(module.DBProvider)
	if !ok || dbp.DB() == nil {
		return
	}
	sqlDB := dbp.DB()

	rows, err := sqlDB.QueryContext(ctx, "SELECT id, repo_url, branch FROM project_repos WHERE project_id = ? AND status = 'pending'", projectID)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()

	var repos []repoRow
	for rows.Next() {
		var r repoRow
		if err := rows.Scan(&r.ID, &r.RepoURL, &r.Branch); err != nil {
			continue
		}
		repos = append(repos, r)
	}

	srcDir := filepath.Join(wsPath, "src")
	for _, repo := range repos {
		// Derive repo name from URL
		repoName := repoNameFromURL(repo.RepoURL)
		clonePath := filepath.Join(srcDir, repoName)

		branch := repo.Branch
		if branch == "" {
			branch = "main"
		}

		cloneURL := repo.RepoURL
		if token := os.Getenv("GITHUB_TOKEN"); token != "" && strings.HasPrefix(repo.RepoURL, "https://") {
			cloneURL = strings.Replace(repo.RepoURL, "https://", "https://"+token+"@", 1)
		}

		cmd := exec.CommandContext(ctx, "git", "clone", "--branch", branch, cloneURL, clonePath)
		if err := cmd.Run(); err != nil {
			// Update status to 'error'
			_, _ = sqlDB.ExecContext(ctx, "UPDATE project_repos SET status = 'error' WHERE id = ?", repo.ID)
			continue
		}

		// Update status to 'cloned' and set clone_path
		_, _ = sqlDB.ExecContext(ctx,
			"UPDATE project_repos SET status = 'cloned', clone_path = ?, last_synced_at = datetime('now') WHERE id = ?",
			clonePath, repo.ID,
		)
	}
}

// repoNameFromURL extracts a repo name from a git URL.
func repoNameFromURL(url string) string {
	// Handle trailing .git
	url = strings.TrimSuffix(url, ".git")
	// Handle trailing slash
	url = strings.TrimSuffix(url, "/")
	// Get last path component
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "repo"
}
