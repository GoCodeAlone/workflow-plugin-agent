package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// GitPRCreateTool creates a GitHub pull request.
type GitPRCreateTool struct{}

func (t *GitPRCreateTool) Name() string        { return "git_pr_create" }
func (t *GitPRCreateTool) Description() string { return "Create a GitHub pull request" }
func (t *GitPRCreateTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo_path": map[string]any{"type": "string", "description": "Path to git repository"},
				"title":     map[string]any{"type": "string", "description": "PR title"},
				"body":      map[string]any{"type": "string", "description": "PR body/description"},
				"head":      map[string]any{"type": "string", "description": "Branch to merge from"},
				"base":      map[string]any{"type": "string", "description": "Branch to merge into (default: main)"},
			},
			"required": []string{"repo_path", "title", "head"},
		},
	}
}

func (t *GitPRCreateTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	repoPath, _ := params["repo_path"].(string)
	title, _ := params["title"].(string)
	body, _ := params["body"].(string)
	head, _ := params["head"].(string)
	base, _ := params["base"].(string)

	if repoPath == "" || title == "" || head == "" {
		return nil, fmt.Errorf("repo_path, title, and head are required")
	}
	if base == "" {
		base = "main"
	}

	// Parse owner/repo from git remote
	owner, repo, err := parseGitRemote(repoPath)
	if err != nil {
		return nil, fmt.Errorf("parse git remote: %w", err)
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable not set")
	}

	// Create PR via GitHub API
	prBody := map[string]any{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}
	bodyJSON, _ := json.Marshal(prBody)

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls", owner, repo)

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, apiURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("github api returned %d: %s", resp.StatusCode, string(respBody))
	}

	var prResp map[string]any
	if err := json.Unmarshal(respBody, &prResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	number, _ := prResp["number"].(float64)
	htmlURL, _ := prResp["html_url"].(string)
	state, _ := prResp["state"].(string)

	return map[string]any{
		"number":   int(number),
		"html_url": htmlURL,
		"state":    state,
	}, nil
}

var remoteURLRegex = regexp.MustCompile(`(?:github\.com[:/])([^/]+)/([^/.]+?)(?:\.git)?$`)

func parseGitRemote(repoPath string) (string, string, error) {
	cmd := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("git remote get-url: %w", err)
	}
	url := strings.TrimSpace(string(out))
	matches := remoteURLRegex.FindStringSubmatch(url)
	if len(matches) < 3 {
		return "", "", fmt.Errorf("cannot parse owner/repo from remote URL: %s", url)
	}
	return matches[1], matches[2], nil
}
