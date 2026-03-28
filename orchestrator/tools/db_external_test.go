package tools

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDBQueryExternalTool_SQLiteFileBased(t *testing.T) {
	tool := &DBQueryExternalTool{}
	ctx := context.Background()

	// Use a temp file-based SQLite DB since in-memory DBs don't share across connections
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open file db: %v", err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = db.Exec("INSERT INTO test (id, name) VALUES (1, 'alice'), (2, 'bob')")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	_ = db.Close()

	result, err := tool.Execute(ctx, map[string]any{
		"connection_string": dbPath,
		"driver":            "sqlite3",
		"query":             "SELECT id, name FROM test ORDER BY id",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	count, _ := m["count"].(int)
	if count != 2 {
		t.Errorf("expected 2 rows, got %d", count)
	}

	columns, _ := m["columns"].([]string)
	if len(columns) != 2 || columns[0] != "id" || columns[1] != "name" {
		t.Errorf("unexpected columns: %v", columns)
	}

	rows, ok := m["rows"].([]map[string]any)
	if !ok {
		t.Fatalf("expected rows []map[string]any, got %T", m["rows"])
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows in result, got %d", len(rows))
	}
}

func TestDBQueryExternalTool_RejectsNonSelect(t *testing.T) {
	tool := &DBQueryExternalTool{}
	ctx := context.Background()

	_, err := tool.Execute(ctx, map[string]any{
		"connection_string": ":memory:",
		"driver":            "sqlite3",
		"query":             "DROP TABLE test",
	})
	if err == nil {
		t.Error("expected error for non-SELECT query")
	}
}

func TestDBQueryExternalTool_RejectsMultipleStatements(t *testing.T) {
	tool := &DBQueryExternalTool{}
	ctx := context.Background()

	_, err := tool.Execute(ctx, map[string]any{
		"connection_string": ":memory:",
		"driver":            "sqlite3",
		"query":             "SELECT 1; DROP TABLE test",
	})
	if err == nil {
		t.Error("expected error for multiple statements")
	}
}

func TestDBQueryExternalTool_RejectsUnsupportedDriver(t *testing.T) {
	tool := &DBQueryExternalTool{}
	ctx := context.Background()

	_, err := tool.Execute(ctx, map[string]any{
		"connection_string": ":memory:",
		"driver":            "mysql",
		"query":             "SELECT 1",
	})
	if err == nil {
		t.Error("expected error for unsupported driver")
	}
}

func TestDBQueryExternalTool_MissingParams(t *testing.T) {
	tool := &DBQueryExternalTool{}
	ctx := context.Background()

	_, err := tool.Execute(ctx, map[string]any{})
	if err == nil {
		t.Error("expected error for missing required parameters")
	}
}

func TestDBQueryExternalTool_Definition(t *testing.T) {
	tool := &DBQueryExternalTool{}
	if tool.Name() != "db_query_external" {
		t.Errorf("expected name db_query_external, got %s", tool.Name())
	}
	def := tool.Definition()
	if def.Name != "db_query_external" {
		t.Errorf("expected definition name db_query_external, got %s", def.Name)
	}
}

func TestDBQueryExternalTool_EmptyResult(t *testing.T) {
	tool := &DBQueryExternalTool{}
	ctx := context.Background()

	tmpDir := t.TempDir()
	dbPath := tmpDir + "/empty.db"

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	_, _ = db.Exec("CREATE TABLE empty_table (id INTEGER PRIMARY KEY)")
	_ = db.Close()

	result, err := tool.Execute(ctx, map[string]any{
		"connection_string": dbPath,
		"driver":            "sqlite",
		"query":             "SELECT id FROM empty_table",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	count, _ := m["count"].(int)
	if count != 0 {
		t.Errorf("expected 0 rows, got %d", count)
	}
}
