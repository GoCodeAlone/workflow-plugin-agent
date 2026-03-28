package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/google/uuid"
)

// CodeReviewTool runs golangci-lint on a Go project and returns structured findings.
type CodeReviewTool struct{}

func (t *CodeReviewTool) Name() string { return "code_review" }
func (t *CodeReviewTool) Description() string {
	return "Run static analysis (golangci-lint) on a Go project and return structured findings"
}
func (t *CodeReviewTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "code_review",
		Description: "Run golangci-lint on a Go project path. Returns lint findings with severity, file, line, and message.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the Go project directory to review",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (t *CodeReviewTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("code_review: 'path' is required")
	}

	if _, err := os.Stat(path); err != nil {
		return map[string]any{"error": fmt.Sprintf("path not found: %s", path), "findings": []any{}, "count": 0, "passed": true}, nil
	}

	lintPath, err := exec.LookPath("golangci-lint")
	if err != nil {
		return t.fallbackGoVet(ctx, path)
	}

	execCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, lintPath, "run", "--out-format", "json", "--timeout", "30s", "./...")
	cmd.Dir = path
	out, _ := cmd.CombinedOutput()

	return t.parseGolangciOutput(out, path)
}

func (t *CodeReviewTool) fallbackGoVet(ctx context.Context, path string) (any, error) {
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "go", "vet", "./...")
	cmd.Dir = path
	out, _ := cmd.CombinedOutput()

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	findings := []map[string]any{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		findings = append(findings, map[string]any{
			"severity": "warning",
			"message":  line,
			"linter":   "go-vet",
		})
	}
	return map[string]any{
		"findings": findings,
		"count":    len(findings),
		"passed":   len(findings) == 0,
		"tool":     "go-vet-fallback",
	}, nil
}

func (t *CodeReviewTool) parseGolangciOutput(out []byte, basePath string) (any, error) {
	type lintIssue struct {
		FromLinter string `json:"FromLinter"`
		Text       string `json:"Text"`
		Severity   string `json:"Severity"`
		Pos        struct {
			Filename string `json:"Filename"`
			Line     int    `json:"Line"`
			Column   int    `json:"Column"`
		} `json:"Pos"`
	}
	type lintOutput struct {
		Issues []lintIssue `json:"Issues"`
	}

	var parsed lintOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return map[string]any{
			"findings": []any{},
			"count":    0,
			"passed":   true,
			"raw":      string(out),
			"tool":     "golangci-lint",
		}, nil
	}

	findings := make([]map[string]any, 0, len(parsed.Issues))
	for _, issue := range parsed.Issues {
		relPath, _ := filepath.Rel(basePath, issue.Pos.Filename)
		if relPath == "" {
			relPath = issue.Pos.Filename
		}
		severity := issue.Severity
		if severity == "" {
			severity = "warning"
		}
		findings = append(findings, map[string]any{
			"severity": severity,
			"file":     relPath,
			"line":     issue.Pos.Line,
			"message":  issue.Text,
			"linter":   issue.FromLinter,
		})
	}

	return map[string]any{
		"findings": findings,
		"count":    len(findings),
		"passed":   len(findings) == 0,
		"tool":     "golangci-lint",
	}, nil
}

// CodeComplexityTool analyzes Go code complexity and identifies tech debt markers.
type CodeComplexityTool struct{}

func (t *CodeComplexityTool) Name() string { return "code_complexity" }
func (t *CodeComplexityTool) Description() string {
	return "Analyze Go code complexity (cyclomatic) and find TODO/FIXME/HACK markers"
}
func (t *CodeComplexityTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "code_complexity",
		Description: "Analyze a Go project for cyclomatic complexity and tech debt markers (TODO, FIXME, HACK). Returns high-complexity functions and debt items.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the Go project directory",
				},
				"threshold": map[string]any{
					"type":        "number",
					"description": "Complexity threshold (default: 10)",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (t *CodeComplexityTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("code_complexity: 'path' is required")
	}

	threshold := 10
	if v, ok := args["threshold"].(float64); ok && v > 0 {
		threshold = int(v)
	}

	if _, err := os.Stat(path); err != nil {
		return map[string]any{"error": fmt.Sprintf("path not found: %s", path)}, nil
	}

	functions := t.findComplexFunctions(ctx, path, threshold)
	todos := t.findDebtMarkers(path)

	debtScore := len(functions)*3 + len(todos)

	return map[string]any{
		"functions":  functions,
		"todos":      todos,
		"debt_score": debtScore,
		"threshold":  threshold,
	}, nil
}

func (t *CodeComplexityTool) findComplexFunctions(ctx context.Context, path string, threshold int) []map[string]any {
	gocycloPath, err := exec.LookPath("gocyclo")
	if err != nil {
		return []map[string]any{}
	}

	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, gocycloPath, "-over", fmt.Sprintf("%d", threshold), path)
	out, _ := cmd.CombinedOutput()

	functions := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// gocyclo output: "N path/file.go:line:col FuncName"
		parts := strings.Fields(line)
		if len(parts) >= 3 {
			functions = append(functions, map[string]any{
				"complexity": parts[0],
				"location":   parts[1],
				"name":       strings.Join(parts[2:], " "),
			})
		}
	}
	return functions
}

func (t *CodeComplexityTool) findDebtMarkers(path string) []map[string]any {
	markers := []map[string]any{}
	patterns := []string{"TODO", "FIXME", "HACK", "XXX", "DEPRECATED"}

	_ = filepath.Walk(path, func(fpath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(fpath, ".go") {
			return nil
		}
		data, err := os.ReadFile(fpath)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, pattern := range patterns {
				if strings.Contains(line, pattern) {
					relPath, _ := filepath.Rel(path, fpath)
					markers = append(markers, map[string]any{
						"file":    relPath,
						"line":    i + 1,
						"text":    strings.TrimSpace(line),
						"pattern": pattern,
					})
					break
				}
			}
		}
		return nil
	})
	return markers
}

// CodeDiffReviewTool runs git diff between two refs and structures the output.
type CodeDiffReviewTool struct{}

func (t *CodeDiffReviewTool) Name() string { return "code_diff_review" }
func (t *CodeDiffReviewTool) Description() string {
	return "Run git diff between two refs and return structured file changes"
}
func (t *CodeDiffReviewTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "code_diff_review",
		Description: "Get a structured diff between two git refs (branches, commits, tags). Returns changed files with added/removed line counts.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo_path": map[string]any{
					"type":        "string",
					"description": "Path to the git repository",
				},
				"base_ref": map[string]any{
					"type":        "string",
					"description": "Base ref (branch, commit, tag) to diff from",
				},
				"head_ref": map[string]any{
					"type":        "string",
					"description": "Head ref to diff to (default: HEAD)",
				},
			},
			"required": []string{"repo_path", "base_ref"},
		},
	}
}

func (t *CodeDiffReviewTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	repoPath, ok := args["repo_path"].(string)
	if !ok || repoPath == "" {
		return nil, fmt.Errorf("code_diff_review: 'repo_path' is required")
	}
	baseRef, ok := args["base_ref"].(string)
	if !ok || baseRef == "" {
		return nil, fmt.Errorf("code_diff_review: 'base_ref' is required")
	}
	headRef, _ := args["head_ref"].(string)
	if headRef == "" {
		headRef = "HEAD"
	}

	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	diffRange := fmt.Sprintf("%s..%s", baseRef, headRef)
	cmd := exec.CommandContext(execCtx, "git", "diff", "--numstat", diffRange)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("git diff failed: %s", string(out))}, nil
	}

	files := []map[string]any{}
	totalAdded, totalRemoved := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 3 {
			added, removed := 0, 0
			if parts[0] != "-" {
				_, _ = fmt.Sscanf(parts[0], "%d", &added)
			}
			if parts[1] != "-" {
				_, _ = fmt.Sscanf(parts[1], "%d", &removed)
			}
			files = append(files, map[string]any{
				"path":    parts[2],
				"added":   added,
				"removed": removed,
			})
			totalAdded += added
			totalRemoved += removed
		}
	}

	return map[string]any{
		"files":         files,
		"file_count":    len(files),
		"total_added":   totalAdded,
		"total_removed": totalRemoved,
		"base_ref":      baseRef,
		"head_ref":      headRef,
	}, nil
}

// ---- GitLogStatsTool --------------------------------------------------------

// GitLogStatsTool analyses git history to find commit frequency, hotspot files,
// and top contributors over a configurable number of days.
type GitLogStatsTool struct{}

func (t *GitLogStatsTool) Name() string { return "git_log_stats" }
func (t *GitLogStatsTool) Description() string {
	return "Analyse git history to find total commits, hotspot files, and top contributors"
}
func (t *GitLogStatsTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "git_log_stats",
		Description: "Analyse git log history for a repository over a period of days. Returns total commit count, hotspot files (most frequently changed), and top contributors by commit count.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo_path": map[string]any{
					"type":        "string",
					"description": "Path to the git repository",
				},
				"days": map[string]any{
					"type":        "integer",
					"description": "Number of days of history to analyse (default: 90)",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of hotspot files and contributors to return (default: 20)",
				},
			},
			"required": []string{"repo_path"},
		},
	}
}

func (t *GitLogStatsTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	repoPath, ok := args["repo_path"].(string)
	if !ok || repoPath == "" {
		return nil, fmt.Errorf("git_log_stats: 'repo_path' is required")
	}
	days := 90
	if v, ok := args["days"].(float64); ok && v > 0 {
		days = int(v)
	}
	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	since := fmt.Sprintf("%d days ago", days)
	timeout := 30 * time.Second

	// 1. Total commits
	totalCommits := 0
	{
		execCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(execCtx, "git", "-C", repoPath, "log", "--format=%H", "--since="+since)
		out, _ := cmd.Output()
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.TrimSpace(line) != "" {
				totalCommits++
			}
		}
	}

	// 2. Hotspot files — count file occurrences across all commits
	fileFreq := map[string]int{}
	{
		execCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(execCtx, "git", "-C", repoPath, "log", "--since="+since, "--name-only", "--pretty=format:")
		out, _ := cmd.Output()
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				fileFreq[line]++
			}
		}
	}

	// 3. Contributors — count commits per author name
	authorFreq := map[string]int{}
	{
		execCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(execCtx, "git", "-C", repoPath, "log", "--since="+since, "--format=%aN")
		out, _ := cmd.Output()
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				authorFreq[line]++
			}
		}
	}

	hotspots := freqMapToSortedList(fileFreq, "file", "changes", limit)
	contributors := freqMapToSortedList(authorFreq, "name", "commits", limit)

	return map[string]any{
		"repo_path":     repoPath,
		"days":          days,
		"total_commits": totalCommits,
		"hotspots":      hotspots,
		"contributors":  contributors,
	}, nil
}

// freqMapToSortedList converts a frequency map into a sorted slice of maps,
// capped at limit entries (highest count first).
func freqMapToSortedList(freq map[string]int, nameKey, countKey string, limit int) []map[string]any {
	type entry struct {
		name  string
		count int
	}
	entries := make([]entry, 0, len(freq))
	for k, v := range freq {
		entries = append(entries, entry{k, v})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].count > entries[j].count
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	result := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		result = append(result, map[string]any{
			nameKey:  e.name,
			countKey: e.count,
		})
	}
	return result
}

// ---- TestCoverageTool -------------------------------------------------------

// TestCoverageTool runs Go tests with coverage profiling and returns per-package
// coverage percentages along with an aggregate total.
type TestCoverageTool struct{}

func (t *TestCoverageTool) Name() string { return "test_coverage" }
func (t *TestCoverageTool) Description() string {
	return "Run Go tests with coverage and return per-package coverage percentages"
}
func (t *TestCoverageTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "test_coverage",
		Description: "Run `go test -coverprofile` on a Go project and return per-package coverage. Aggregates total statement coverage across all packages.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the Go project directory (must contain go.mod)",
				},
				"packages": map[string]any{
					"type":        "string",
					"description": "Package pattern to test (default: ./...)",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (t *TestCoverageTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("test_coverage: 'path' is required")
	}
	packages := "./..."
	if v, ok := args["packages"].(string); ok && v != "" {
		packages = v
	}

	// Write coverprofile to a temp file
	tmpFile := filepath.Join(os.TempDir(), "ratchet-cover-"+uuid.New().String()+".out")
	defer func() { _ = os.Remove(tmpFile) }()

	execCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "go", "test", "-coverprofile="+tmpFile, "-count=1", packages)
	cmd.Dir = path
	out, _ := cmd.CombinedOutput()

	// Read coverprofile
	coverData, err := os.ReadFile(tmpFile)
	if err != nil {
		// Tests may have failed or produced no coverage; return the raw output as context
		return map[string]any{
			"error":    fmt.Sprintf("coverage file not produced: %v", err),
			"raw":      string(out),
			"packages": []any{},
		}, nil
	}

	return t.parseCoverProfile(coverData)
}

// parseCoverProfile parses a Go coverage profile and returns per-package
// statement coverage statistics.
//
// Each data line has the format:
//
//	file.go:startLine.startCol,endLine.endCol numStatements count
func (t *TestCoverageTool) parseCoverProfile(data []byte) (any, error) {
	type pkgStats struct {
		statements int
		covered    int
	}
	pkgMap := map[string]*pkgStats{}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		// Split off the last two fields (numStatements count)
		lastSpace := strings.LastIndex(line, " ")
		if lastSpace < 0 {
			continue
		}
		countStr := line[lastSpace+1:]
		rest := line[:lastSpace]
		secondLastSpace := strings.LastIndex(rest, " ")
		if secondLastSpace < 0 {
			continue
		}
		stmtStr := rest[secondLastSpace+1:]
		fileRange := rest[:secondLastSpace]

		var stmts, count int
		if _, err := fmt.Sscanf(stmtStr, "%d", &stmts); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(countStr, "%d", &count); err != nil {
			continue
		}

		// Derive package from file path (everything before last '/')
		pkg := fileRange
		if idx := strings.LastIndex(fileRange, "/"); idx >= 0 {
			pkg = fileRange[:idx]
		}
		// Strip trailing position info if present (file.go:L.C,L.C → file.go)
		if idx := strings.Index(pkg, ":"); idx >= 0 {
			pkg = pkg[:idx]
		}

		if _, exists := pkgMap[pkg]; !exists {
			pkgMap[pkg] = &pkgStats{}
		}
		pkgMap[pkg].statements += stmts
		if count > 0 {
			pkgMap[pkg].covered += stmts
		}
	}

	totalStmts, totalCovered := 0, 0
	uncoveredPkgs := []string{}
	pkgList := make([]map[string]any, 0, len(pkgMap))

	for name, stats := range pkgMap {
		cov := 0.0
		if stats.statements > 0 {
			cov = float64(stats.covered) / float64(stats.statements) * 100
		}
		if stats.covered == 0 && stats.statements > 0 {
			uncoveredPkgs = append(uncoveredPkgs, name)
		}
		pkgList = append(pkgList, map[string]any{
			"package":    name,
			"coverage":   fmt.Sprintf("%.1f", cov),
			"statements": stats.statements,
			"covered":    stats.covered,
		})
		totalStmts += stats.statements
		totalCovered += stats.covered
	}

	totalCoverage := 0.0
	if totalStmts > 0 {
		totalCoverage = float64(totalCovered) / float64(totalStmts) * 100
	}

	return map[string]any{
		"packages":           pkgList,
		"total_coverage":     fmt.Sprintf("%.1f", totalCoverage),
		"uncovered_packages": uncoveredPkgs,
	}, nil
}
