package tools

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func setupDataDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE test_table (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		value REAL
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	for i := 0; i < 100; i++ {
		_, _ = db.Exec("INSERT INTO test_table (name, value) VALUES (?, ?)", "item", float64(i))
	}
	return db
}

func TestDBAnalyzeTool_Definition(t *testing.T) {
	tool := &DBAnalyzeTool{}
	if tool.Name() != "db_analyze" {
		t.Fatalf("expected name db_analyze, got %s", tool.Name())
	}
}

func TestDBAnalyzeTool_Execute(t *testing.T) {
	db := setupDataDB(t)
	tool := &DBAnalyzeTool{DB: db}
	result, err := tool.Execute(context.Background(), map[string]any{
		"query": "SELECT * FROM test_table WHERE name = 'item'",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if _, ok := m["plan"]; !ok {
		t.Fatal("expected 'plan' key in result")
	}
}

func TestDBAnalyzeTool_Execute_MissingQuery(t *testing.T) {
	db := setupDataDB(t)
	tool := &DBAnalyzeTool{DB: db}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing query")
	}
}

func TestDBAnalyzeTool_Execute_NonSelect(t *testing.T) {
	db := setupDataDB(t)
	tool := &DBAnalyzeTool{DB: db}
	result, err := tool.Execute(context.Background(), map[string]any{
		"query": "DELETE FROM test_table",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if _, ok := m["error"]; !ok {
		t.Fatal("expected error for non-SELECT query")
	}
}

func TestDBHealthCheckTool_Definition(t *testing.T) {
	tool := &DBHealthCheckTool{}
	if tool.Name() != "db_health_check" {
		t.Fatalf("expected name db_health_check, got %s", tool.Name())
	}
}

func TestDBHealthCheckTool_Execute(t *testing.T) {
	db := setupDataDB(t)
	tool := &DBHealthCheckTool{DB: db}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if _, ok := m["integrity"]; !ok {
		t.Fatal("expected 'integrity' key")
	}
	tables, ok := m["tables"].([]map[string]any)
	if !ok {
		t.Fatal("expected 'tables' slice")
	}
	if len(tables) == 0 {
		t.Fatal("expected at least one table")
	}
}

func TestDBHealthCheckTool_Execute_NilDB(t *testing.T) {
	tool := &DBHealthCheckTool{}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if _, ok := m["error"]; !ok {
		t.Fatal("expected error key when no DB")
	}
}

// ---------- SchemaInspectTool ----------

func setupSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE test_items (
		id         INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		value      REAL,
		created_at TEXT
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE other_table (
		pk   INTEGER PRIMARY KEY,
		data TEXT
	)`)
	if err != nil {
		t.Fatalf("create other_table: %v", err)
	}
	for i := 0; i < 5; i++ {
		_, _ = db.Exec("INSERT INTO test_items (name, value, created_at) VALUES (?, ?, ?)",
			"item", float64(i)*1.5, "2024-01-01")
	}
	return db
}

func TestSchemaInspectTool_Name(t *testing.T) {
	tool := &SchemaInspectTool{}
	if tool.Name() != "schema_inspect" {
		t.Fatalf("expected name schema_inspect, got %s", tool.Name())
	}
}

func TestSchemaInspectTool_Execute(t *testing.T) {
	db := setupSchemaDB(t)
	tool := &SchemaInspectTool{DB: db}

	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	tableCount, ok := m["table_count"].(int)
	if !ok {
		t.Fatalf("expected table_count int, got %T", m["table_count"])
	}
	if tableCount < 2 {
		t.Errorf("expected at least 2 tables, got %d", tableCount)
	}

	tables, ok := m["tables"].([]map[string]any)
	if !ok {
		t.Fatalf("expected tables []map[string]any, got %T", m["tables"])
	}

	// Find test_items in the result.
	var testItemsTable map[string]any
	for _, tbl := range tables {
		if tbl["name"] == "test_items" {
			testItemsTable = tbl
			break
		}
	}
	if testItemsTable == nil {
		t.Fatal("expected to find test_items table in result")
	}

	rowCount, ok := testItemsTable["row_count"].(int)
	if !ok {
		t.Fatalf("expected row_count int, got %T", testItemsTable["row_count"])
	}
	if rowCount != 5 {
		t.Errorf("expected row_count 5, got %d", rowCount)
	}

	columns, ok := testItemsTable["columns"].([]map[string]any)
	if !ok {
		t.Fatalf("expected columns []map[string]any, got %T", testItemsTable["columns"])
	}
	if len(columns) != 4 {
		t.Errorf("expected 4 columns, got %d", len(columns))
	}

	// Verify column names are present.
	colNames := map[string]bool{}
	for _, col := range columns {
		if name, ok := col["name"].(string); ok {
			colNames[name] = true
		}
	}
	for _, expected := range []string{"id", "name", "value", "created_at"} {
		if !colNames[expected] {
			t.Errorf("expected column %q to be present", expected)
		}
	}
}

func TestSchemaInspectTool_Execute_SpecificTable(t *testing.T) {
	db := setupSchemaDB(t)
	tool := &SchemaInspectTool{DB: db}

	result, err := tool.Execute(context.Background(), map[string]any{"table": "other_table"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	tableCount, _ := m["table_count"].(int)
	if tableCount != 1 {
		t.Errorf("expected table_count 1 when filtering by name, got %d", tableCount)
	}

	tables, ok := m["tables"].([]map[string]any)
	if !ok || len(tables) != 1 {
		t.Fatalf("expected exactly 1 table, got %v", m["tables"])
	}
	if tables[0]["name"] != "other_table" {
		t.Errorf("expected table name other_table, got %v", tables[0]["name"])
	}
}

func TestSchemaInspectTool_Execute_NilDB(t *testing.T) {
	tool := &SchemaInspectTool{}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if _, ok := m["error"]; !ok {
		t.Fatal("expected error key when DB is nil")
	}
}

// ---------- DataProfileTool ----------

func setupProfileDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE profile_data (
		id    INTEGER PRIMARY KEY,
		label TEXT,
		score REAL
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	// Insert rows: 3 with values, 2 with NULLs for label/score.
	_, _ = db.Exec("INSERT INTO profile_data (label, score) VALUES ('alpha', 10.0)")
	_, _ = db.Exec("INSERT INTO profile_data (label, score) VALUES ('beta', 20.0)")
	_, _ = db.Exec("INSERT INTO profile_data (label, score) VALUES ('gamma', 30.0)")
	_, _ = db.Exec("INSERT INTO profile_data (label, score) VALUES (NULL, NULL)")
	_, _ = db.Exec("INSERT INTO profile_data (label, score) VALUES (NULL, NULL)")
	return db
}

func TestDataProfileTool_Name(t *testing.T) {
	tool := &DataProfileTool{}
	if tool.Name() != "data_profile" {
		t.Fatalf("expected name data_profile, got %s", tool.Name())
	}
}

func TestDataProfileTool_Execute(t *testing.T) {
	db := setupProfileDB(t)
	tool := &DataProfileTool{DB: db}

	result, err := tool.Execute(context.Background(), map[string]any{"table": "profile_data"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	// Verify top-level keys and values.
	if m["table"] != "profile_data" {
		t.Errorf("expected table 'profile_data', got %v", m["table"])
	}

	rowCount, _ := m["row_count"].(int)
	if rowCount != 5 {
		t.Errorf("expected row_count 5, got %d", rowCount)
	}

	if _, ok := m["sample_size"]; !ok {
		t.Error("expected 'sample_size' key in result")
	}

	columns, ok := m["columns"].([]map[string]any)
	if !ok {
		t.Fatalf("expected columns []map[string]any, got %T", m["columns"])
	}
	// Table has 3 columns: id, label, score.
	if len(columns) != 3 {
		t.Errorf("expected 3 column profiles, got %d", len(columns))
	}

	// Each column profile must have name, type, null_count, distinct_count,
	// null_percent keys (statistics may be 0 depending on SQL dialect support).
	colNames := map[string]bool{}
	for _, col := range columns {
		name, ok := col["name"].(string)
		if !ok || name == "" {
			t.Error("column profile missing 'name' string")
			continue
		}
		colNames[name] = true
		if _, ok := col["type"]; !ok {
			t.Errorf("column %q missing 'type' key", name)
		}
		if _, ok := col["null_count"]; !ok {
			t.Errorf("column %q missing 'null_count' key", name)
		}
		if _, ok := col["distinct_count"]; !ok {
			t.Errorf("column %q missing 'distinct_count' key", name)
		}
		if _, ok := col["null_percent"]; !ok {
			t.Errorf("column %q missing 'null_percent' key", name)
		}
	}

	// Verify expected column names are present.
	for _, expected := range []string{"id", "label", "score"} {
		if !colNames[expected] {
			t.Errorf("expected column profile for %q to be present", expected)
		}
	}
}

func TestDataProfileTool_Execute_MissingTable(t *testing.T) {
	db := setupProfileDB(t)
	tool := &DataProfileTool{DB: db}

	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing table parameter")
	}
}

func TestDataProfileTool_Execute_InvalidTable(t *testing.T) {
	db := setupProfileDB(t)
	tool := &DataProfileTool{DB: db}

	result, err := tool.Execute(context.Background(), map[string]any{"table": "nonexistent_table"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if _, ok := m["error"]; !ok {
		t.Fatal("expected error key for non-existent table")
	}
}

func TestDataProfileTool_Execute_NilDB(t *testing.T) {
	tool := &DataProfileTool{}
	result, err := tool.Execute(context.Background(), map[string]any{"table": "any"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if _, ok := m["error"]; !ok {
		t.Fatal("expected error key when DB is nil")
	}
}
