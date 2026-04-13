package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// validatePath ensures the path is within the workspace and prevents traversal.
// relPath may be relative (joined with workspace) or absolute (validated directly).
func validatePath(workspace, relPath string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("no workspace configured")
	}
	var abs string
	if filepath.IsAbs(relPath) {
		// Absolute path: validate directly against workspace boundary.
		abs = filepath.Clean(relPath)
	} else {
		abs = filepath.Join(workspace, relPath)
	}
	absResolved, err := filepath.Abs(abs)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	wsResolved, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("invalid workspace: %w", err)
	}
	if !strings.HasPrefix(absResolved, wsResolved+string(filepath.Separator)) && absResolved != wsResolved {
		return "", fmt.Errorf("path traversal not allowed: %s", relPath)
	}
	return absResolved, nil
}

// FileReadTool reads a file from the project workspace.
type FileReadTool struct {
	Workspace string
}

func (t *FileReadTool) Name() string        { return "file_read" }
func (t *FileReadTool) Description() string { return "Read a file from the project workspace" }
func (t *FileReadTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Relative path to the file"},
			},
			"required": []string{"path"},
		},
	}
}
func (t *FileReadTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	workspace := t.Workspace
	if ws, ok := WorkspacePathFromContext(ctx); ok {
		workspace = ws
	}
	absPath, err := validatePath(workspace, path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	return string(data), nil
}

// FileWriteTool writes a file to the project workspace.
type FileWriteTool struct {
	Workspace string
}

func (t *FileWriteTool) Name() string        { return "file_write" }
func (t *FileWriteTool) Description() string { return "Write a file to the project workspace" }
func (t *FileWriteTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Relative path to the file"},
				"content": map[string]any{"type": "string", "description": "File content to write"},
			},
			"required": []string{"path", "content"},
		},
	}
}
func (t *FileWriteTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	workspace := t.Workspace
	if ws, ok := WorkspacePathFromContext(ctx); ok {
		workspace = ws
	}
	absPath, err := validatePath(workspace, path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}
	return map[string]any{"path": path, "bytes_written": len(content)}, nil
}

// FileListTool lists files in the project workspace.
type FileListTool struct {
	Workspace string
}

func (t *FileListTool) Name() string        { return "file_list" }
func (t *FileListTool) Description() string { return "List files in the project workspace" }
func (t *FileListTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Relative directory path (default: root)"},
			},
		},
	}
}
func (t *FileListTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	workspace := t.Workspace
	if ws, ok := WorkspacePathFromContext(ctx); ok {
		workspace = ws
	}
	absPath, err := validatePath(workspace, path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}
	var files []map[string]any
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, map[string]any{
			"name":   e.Name(),
			"is_dir": e.IsDir(),
			"size":   info.Size(),
		})
	}
	return files, nil
}
