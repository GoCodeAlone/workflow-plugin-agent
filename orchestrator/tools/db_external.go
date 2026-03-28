package tools

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	_ "modernc.org/sqlite"
)

// DBQueryExternalTool allows agents to query external databases (SELECT only).
type DBQueryExternalTool struct{}

func (t *DBQueryExternalTool) Name() string { return "db_query_external" }
func (t *DBQueryExternalTool) Description() string {
	return "Query an external database (SELECT only)"
}
func (t *DBQueryExternalTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: "Query an external SQLite or PostgreSQL database using a SELECT statement. Returns rows, column names, and row count. Only SELECT queries are permitted.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"connection_string": map[string]any{
					"type":        "string",
					"description": "Database connection string (file path for SQLite, DSN for PostgreSQL)",
				},
				"driver": map[string]any{
					"type":        "string",
					"description": "Database driver: sqlite3 or postgres",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "SQL SELECT query to execute",
				},
				"params": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional query parameters for parameterized queries",
				},
			},
			"required": []string{"connection_string", "driver", "query"},
		},
	}
}

func (t *DBQueryExternalTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	connStr, _ := args["connection_string"].(string)
	driver, _ := args["driver"].(string)
	query, _ := args["query"].(string)

	if connStr == "" || driver == "" || query == "" {
		return nil, fmt.Errorf("connection_string, driver, and query are required")
	}

	// Normalize driver name
	switch driver {
	case "sqlite3", "sqlite":
		driver = "sqlite"
	case "postgres", "pgx":
		driver = "pgx"
	default:
		return nil, fmt.Errorf("unsupported driver %q: use sqlite3 or postgres", driver)
	}

	// Validate SELECT-only
	trimmed := strings.TrimSpace(strings.ToUpper(query))
	if !strings.HasPrefix(trimmed, "SELECT") {
		return nil, fmt.Errorf("only SELECT queries are allowed")
	}
	// Check for multiple statements (basic guard)
	cleaned := strings.TrimRight(query, "; \t\n")
	if strings.Contains(cleaned, ";") {
		return nil, fmt.Errorf("multiple statements not allowed")
	}

	// Parse optional params
	var queryParams []any
	if p, ok := args["params"].([]any); ok {
		queryParams = p
	}

	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	db, err := sql.Open(driver, connStr)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(execCtx, query, queryParams...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				val = string(b)
			}
			row[col] = val
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	if results == nil {
		results = []map[string]any{}
	}

	return map[string]any{
		"rows":    results,
		"count":   len(results),
		"columns": columns,
	}, nil
}
