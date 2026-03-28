package tools

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// helper: create an in-memory SQLite DB with the tasks table.
func setupTasksDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending',
		priority INTEGER NOT NULL DEFAULT 1,
		assigned_to TEXT NOT NULL DEFAULT '',
		team_id TEXT NOT NULL DEFAULT '',
		project_id TEXT NOT NULL DEFAULT '',
		parent_id TEXT NOT NULL DEFAULT '',
		depends_on TEXT NOT NULL DEFAULT '[]',
		labels TEXT NOT NULL DEFAULT '[]',
		metadata TEXT NOT NULL DEFAULT '{}',
		result TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT (datetime('now')),
		updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
		started_at DATETIME,
		completed_at DATETIME
	)`)
	if err != nil {
		t.Fatalf("create tasks table: %v", err)
	}
	return db
}

// helper: create an in-memory SQLite DB with the messages table.
func setupMessagesDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL DEFAULT 'direct',
		from_agent TEXT NOT NULL DEFAULT '',
		to_agent TEXT NOT NULL DEFAULT '',
		team_id TEXT NOT NULL DEFAULT '',
		subject TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL DEFAULT '',
		reply_to TEXT NOT NULL DEFAULT '',
		metadata TEXT NOT NULL DEFAULT '{}',
		created_at DATETIME NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		t.Fatalf("create messages table: %v", err)
	}
	return db
}

// helper: create a temp workspace directory.
func setupWorkspace(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tools-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// ---------- FileReadTool ----------

func TestFileReadTool_Definition(t *testing.T) {
	tool := &FileReadTool{Workspace: "/tmp"}
	def := tool.Definition()
	if def.Name != "file_read" {
		t.Errorf("expected name %q, got %q", "file_read", def.Name)
	}
	if def.Description == "" {
		t.Error("expected non-empty description")
	}
	if def.Parameters == nil {
		t.Error("expected non-nil parameters")
	}
}

func TestFileReadTool_Execute(t *testing.T) {
	ws := setupWorkspace(t)
	tool := &FileReadTool{Workspace: ws}
	ctx := context.Background()

	t.Run("read existing file", func(t *testing.T) {
		content := "hello world"
		if err := os.WriteFile(filepath.Join(ws, "test.txt"), []byte(content), 0o644); err != nil {
			t.Fatalf("write test file: %v", err)
		}
		result, err := tool.Execute(ctx, map[string]any{"path": "test.txt"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != content {
			t.Errorf("expected %q, got %q", content, result)
		}
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"path": "../../../etc/passwd"})
		if err == nil {
			t.Fatal("expected error for path traversal")
		}
	})

	t.Run("missing path arg", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing path")
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"path": "no-such-file.txt"})
		if err == nil {
			t.Fatal("expected error for non-existent file")
		}
	})
}

// ---------- FileWriteTool ----------

func TestFileWriteTool_Definition(t *testing.T) {
	tool := &FileWriteTool{Workspace: "/tmp"}
	def := tool.Definition()
	if def.Name != "file_write" {
		t.Errorf("expected name %q, got %q", "file_write", def.Name)
	}
	if def.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestFileWriteTool_Execute(t *testing.T) {
	ws := setupWorkspace(t)
	tool := &FileWriteTool{Workspace: ws}
	ctx := context.Background()

	t.Run("write file", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{
			"path":    "output.txt",
			"content": "written content",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("expected map result, got %T", result)
		}
		if m["path"] != "output.txt" {
			t.Errorf("expected path %q, got %q", "output.txt", m["path"])
		}
		if m["bytes_written"] != 15 {
			t.Errorf("expected bytes_written 15, got %v", m["bytes_written"])
		}
		// verify on disk
		data, err := os.ReadFile(filepath.Join(ws, "output.txt"))
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(data) != "written content" {
			t.Errorf("expected %q on disk, got %q", "written content", string(data))
		}
	})

	t.Run("creates parent directories", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{
			"path":    "sub/dir/deep.txt",
			"content": "nested",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(ws, "sub", "dir", "deep.txt"))
		if err != nil {
			t.Fatalf("read back nested: %v", err)
		}
		if string(data) != "nested" {
			t.Errorf("expected %q, got %q", "nested", string(data))
		}
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{
			"path":    "../../escape.txt",
			"content": "bad",
		})
		if err == nil {
			t.Fatal("expected error for path traversal")
		}
	})

	t.Run("missing path", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"content": "no path"})
		if err == nil {
			t.Fatal("expected error for missing path")
		}
	})
}

// ---------- FileListTool ----------

func TestFileListTool_Definition(t *testing.T) {
	tool := &FileListTool{Workspace: "/tmp"}
	def := tool.Definition()
	if def.Name != "file_list" {
		t.Errorf("expected name %q, got %q", "file_list", def.Name)
	}
}

func TestFileListTool_Execute(t *testing.T) {
	ws := setupWorkspace(t)
	tool := &FileListTool{Workspace: ws}
	ctx := context.Background()

	// populate workspace
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ws, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("list populated directory", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{"path": "."})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		files, ok := result.([]map[string]any)
		if !ok {
			t.Fatalf("expected []map[string]any, got %T", result)
		}
		if len(files) < 2 {
			t.Fatalf("expected at least 2 entries, got %d", len(files))
		}
		// verify entries have expected keys
		for _, f := range files {
			if _, ok := f["name"]; !ok {
				t.Error("entry missing 'name'")
			}
			if _, ok := f["is_dir"]; !ok {
				t.Error("entry missing 'is_dir'")
			}
			if _, ok := f["size"]; !ok {
				t.Error("entry missing 'size'")
			}
		}
		// check subdir is reported as dir
		found := false
		for _, f := range files {
			if f["name"] == "subdir" {
				found = true
				if f["is_dir"] != true {
					t.Error("expected subdir to be reported as directory")
				}
			}
		}
		if !found {
			t.Error("expected to find 'subdir' in listing")
		}
	})

	t.Run("default path lists workspace root", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		files, ok := result.([]map[string]any)
		if !ok {
			t.Fatalf("expected []map[string]any, got %T", result)
		}
		if len(files) < 2 {
			t.Errorf("expected at least 2 entries, got %d", len(files))
		}
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"path": "../../.."})
		if err == nil {
			t.Fatal("expected error for path traversal")
		}
	})
}

// ---------- ShellExecTool ----------

func TestShellExecTool_Definition(t *testing.T) {
	tool := &ShellExecTool{Workspace: "/tmp"}
	def := tool.Definition()
	if def.Name != "shell_exec" {
		t.Errorf("expected name %q, got %q", "shell_exec", def.Name)
	}
}

func TestShellExecTool_Execute(t *testing.T) {
	ws := setupWorkspace(t)
	tool := &ShellExecTool{Workspace: ws}
	ctx := context.Background()

	t.Run("echo hello", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{"command": "echo hello"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("expected map, got %T", result)
		}
		if m["stdout"] != "hello\n" {
			t.Errorf("expected stdout %q, got %q", "hello\n", m["stdout"])
		}
		if m["exit_code"] != 0 {
			t.Errorf("expected exit_code 0, got %v", m["exit_code"])
		}
	})

	t.Run("failing command", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{"command": "exit 42"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m := result.(map[string]any)
		if m["exit_code"] != 42 {
			t.Errorf("expected exit_code 42, got %v", m["exit_code"])
		}
	})

	t.Run("timeout capped at 300", func(t *testing.T) {
		// Just verify it doesn't error; the cap is internal.
		result, err := tool.Execute(ctx, map[string]any{
			"command": "echo capped",
			"timeout": float64(999),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m := result.(map[string]any)
		if m["stdout"] != "capped\n" {
			t.Errorf("expected stdout %q, got %q", "capped\n", m["stdout"])
		}
	})

	t.Run("empty command", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"command": ""})
		if err == nil {
			t.Fatal("expected error for empty command")
		}
	})

	t.Run("command runs in workspace dir", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{"command": "pwd"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m := result.(map[string]any)
		stdout := m["stdout"].(string)
		// resolve symlinks for comparison (temp dirs may be symlinked)
		resolvedWs, _ := filepath.EvalSymlinks(ws)
		resolvedStdout, _ := filepath.EvalSymlinks(stdout[:len(stdout)-1]) // strip trailing newline
		if resolvedStdout != resolvedWs {
			t.Errorf("expected working dir %q, got %q", resolvedWs, resolvedStdout)
		}
	})
}

// ---------- TaskCreateTool ----------

func TestTaskCreateTool_Definition(t *testing.T) {
	tool := &TaskCreateTool{DB: nil}
	def := tool.Definition()
	if def.Name != "task_create" {
		t.Errorf("expected name %q, got %q", "task_create", def.Name)
	}
}

func TestTaskCreateTool_Execute(t *testing.T) {
	db := setupTasksDB(t)
	tool := &TaskCreateTool{DB: db}
	ctx := context.Background()

	t.Run("create task with title", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{"title": "Test Task"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("expected map, got %T", result)
		}
		id, ok := m["id"].(string)
		if !ok || id == "" {
			t.Error("expected non-empty id")
		}
		if m["title"] != "Test Task" {
			t.Errorf("expected title %q, got %q", "Test Task", m["title"])
		}
		if m["status"] != "pending" {
			t.Errorf("expected status %q, got %q", "pending", m["status"])
		}

		// verify in DB
		var title, status string
		err = db.QueryRow("SELECT title, status FROM tasks WHERE id = ?", id).Scan(&title, &status)
		if err != nil {
			t.Fatalf("query task: %v", err)
		}
		if title != "Test Task" {
			t.Errorf("DB title: expected %q, got %q", "Test Task", title)
		}
		if status != "pending" {
			t.Errorf("DB status: expected %q, got %q", "pending", status)
		}
	})

	t.Run("missing title", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing title")
		}
	})

	t.Run("create with all fields", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{
			"title":       "Full Task",
			"description": "A detailed description",
			"priority":    float64(5),
			"assigned_to": "agent-1",
			"project_id":  "proj-1",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m := result.(map[string]any)
		id := m["id"].(string)

		var desc string
		var priority int
		var assignedTo, projectID string
		err = db.QueryRow("SELECT description, priority, assigned_to, project_id FROM tasks WHERE id = ?", id).
			Scan(&desc, &priority, &assignedTo, &projectID)
		if err != nil {
			t.Fatalf("query task: %v", err)
		}
		if desc != "A detailed description" {
			t.Errorf("expected description %q, got %q", "A detailed description", desc)
		}
		if priority != 5 {
			t.Errorf("expected priority 5, got %d", priority)
		}
		if assignedTo != "agent-1" {
			t.Errorf("expected assigned_to %q, got %q", "agent-1", assignedTo)
		}
		if projectID != "proj-1" {
			t.Errorf("expected project_id %q, got %q", "proj-1", projectID)
		}
	})
}

// ---------- TaskUpdateTool ----------

func TestTaskUpdateTool_Definition(t *testing.T) {
	tool := &TaskUpdateTool{DB: nil}
	def := tool.Definition()
	if def.Name != "task_update" {
		t.Errorf("expected name %q, got %q", "task_update", def.Name)
	}
}

func TestTaskUpdateTool_Execute(t *testing.T) {
	db := setupTasksDB(t)
	ctx := context.Background()

	// seed a task
	createTool := &TaskCreateTool{DB: db}
	created, err := createTool.Execute(ctx, map[string]any{"title": "Update Me"})
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	taskID := created.(map[string]any)["id"].(string)

	tool := &TaskUpdateTool{DB: db}

	t.Run("update status", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{
			"id":     taskID,
			"status": "in_progress",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m := result.(map[string]any)
		if m["updated"] != true {
			t.Error("expected updated=true")
		}

		var status string
		err = db.QueryRow("SELECT status FROM tasks WHERE id = ?", taskID).Scan(&status)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if status != "in_progress" {
			t.Errorf("expected status %q, got %q", "in_progress", status)
		}
	})

	t.Run("update result", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{
			"id":     taskID,
			"result": "task completed successfully",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var result string
		err = db.QueryRow("SELECT result FROM tasks WHERE id = ?", taskID).Scan(&result)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if result != "task completed successfully" {
			t.Errorf("expected result %q, got %q", "task completed successfully", result)
		}
	})

	t.Run("missing id", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"status": "done"})
		if err == nil {
			t.Fatal("expected error for missing id")
		}
	})
}

// ---------- MessageSendTool ----------

func TestMessageSendTool_Definition(t *testing.T) {
	tool := &MessageSendTool{DB: nil}
	def := tool.Definition()
	if def.Name != "message_send" {
		t.Errorf("expected name %q, got %q", "message_send", def.Name)
	}
}

func TestMessageSendTool_Execute(t *testing.T) {
	db := setupMessagesDB(t)
	tool := &MessageSendTool{DB: db}
	ctx := context.Background()

	t.Run("send message", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]any{
			"to":      "agent-2",
			"content": "Hello from test",
			"subject": "Test Subject",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("expected map, got %T", result)
		}
		id, ok := m["id"].(string)
		if !ok || id == "" {
			t.Error("expected non-empty id")
		}
		if m["sent"] != true {
			t.Error("expected sent=true")
		}

		// verify in DB
		var toAgent, content, subject string
		err = db.QueryRow("SELECT to_agent, content, subject FROM messages WHERE id = ?", id).
			Scan(&toAgent, &content, &subject)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if toAgent != "agent-2" {
			t.Errorf("expected to_agent %q, got %q", "agent-2", toAgent)
		}
		if content != "Hello from test" {
			t.Errorf("expected content %q, got %q", "Hello from test", content)
		}
		if subject != "Test Subject" {
			t.Errorf("expected subject %q, got %q", "Test Subject", subject)
		}
	})

	t.Run("missing to", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"content": "no recipient"})
		if err == nil {
			t.Fatal("expected error for missing to")
		}
	})

	t.Run("missing content", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"to": "agent-2"})
		if err == nil {
			t.Fatal("expected error for missing content")
		}
	})
}

// ---------- WebFetchTool ----------

func TestWebFetchTool_Definition(t *testing.T) {
	tool := &WebFetchTool{}
	def := tool.Definition()
	if def.Name != "web_fetch" {
		t.Errorf("expected name %q, got %q", "web_fetch", def.Name)
	}
}

func TestWebFetchTool_Execute(t *testing.T) {
	tool := &WebFetchTool{}
	ctx := context.Background()

	t.Run("fetch from test server", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, "test response body")
		}))
		defer srv.Close()

		result, err := tool.Execute(ctx, map[string]any{"url": srv.URL})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("expected map, got %T", result)
		}
		if m["status"] != 200 {
			t.Errorf("expected status 200, got %v", m["status"])
		}
		if m["body"] != "test response body" {
			t.Errorf("expected body %q, got %q", "test response body", m["body"])
		}
		ct, _ := m["content_type"].(string)
		if ct != "text/plain" {
			t.Errorf("expected content_type %q, got %q", "text/plain", ct)
		}
	})

	t.Run("missing url", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing url")
		}
	})

	t.Run("invalid url", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"url": "not-a-url"})
		if err == nil {
			t.Fatal("expected error for invalid url")
		}
	})
}

// ---------- validatePath (tested through tools) ----------

func TestValidatePath_EmptyWorkspace(t *testing.T) {
	tool := &FileReadTool{Workspace: ""}
	_, err := tool.Execute(context.Background(), map[string]any{"path": "file.txt"})
	if err == nil {
		t.Fatal("expected error for empty workspace")
	}
}

// ---------- All tools implement the interface ----------

func TestAllToolNames(t *testing.T) {
	ws := setupWorkspace(t)
	db := setupTasksDB(t)

	tools := []struct {
		name string
		tool interface{ Name() string }
	}{
		{"file_read", &FileReadTool{Workspace: ws}},
		{"file_write", &FileWriteTool{Workspace: ws}},
		{"file_list", &FileListTool{Workspace: ws}},
		{"shell_exec", &ShellExecTool{Workspace: ws}},
		{"task_create", &TaskCreateTool{DB: db}},
		{"task_update", &TaskUpdateTool{DB: db}},
		{"message_send", &MessageSendTool{DB: db}},
		{"web_fetch", &WebFetchTool{}},
	}

	for _, tc := range tools {
		t.Run(tc.name, func(t *testing.T) {
			if tc.tool.Name() != tc.name {
				t.Errorf("expected name %q, got %q", tc.name, tc.tool.Name())
			}
		})
	}
}
