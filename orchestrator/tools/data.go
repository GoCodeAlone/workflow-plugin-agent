package tools

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// DBAnalyzeTool runs EXPLAIN QUERY PLAN on SQL queries for optimization analysis.
type DBAnalyzeTool struct {
	DB *sql.DB
}

func (t *DBAnalyzeTool) Name() string { return "db_analyze" }
func (t *DBAnalyzeTool) Description() string {
	return "Analyze SQL query execution plans to identify optimization opportunities"
}
func (t *DBAnalyzeTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "db_analyze",
		Description: "Run EXPLAIN QUERY PLAN on a SQL query to analyze execution strategy. Identifies full table scans, index usage, and estimated cost.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The SQL SELECT query to analyze",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (t *DBAnalyzeTool) Execute(_ context.Context, args map[string]any) (any, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return nil, fmt.Errorf("db_analyze: 'query' is required")
	}

	if t.DB == nil {
		return map[string]any{"error": "database not configured"}, nil
	}

	normalized := strings.TrimSpace(strings.ToUpper(query))
	if !strings.HasPrefix(normalized, "SELECT") {
		return map[string]any{"error": "only SELECT queries can be analyzed"}, nil
	}

	rows, err := t.DB.Query("EXPLAIN QUERY PLAN " + query)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("explain failed: %v", err)}, nil
	}
	defer func() { _ = rows.Close() }()

	planLines := []string{}
	fullScan := false
	indexUsed := ""

	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			continue
		}
		planLines = append(planLines, detail)
		if strings.Contains(strings.ToUpper(detail), "SCAN") {
			fullScan = true
		}
		if strings.Contains(strings.ToUpper(detail), "INDEX") {
			parts := strings.Fields(detail)
			for i, p := range parts {
				if strings.ToUpper(p) == "INDEX" && i+1 < len(parts) {
					indexUsed = parts[i+1]
				}
			}
		}
	}

	return map[string]any{
		"plan":       strings.Join(planLines, "\n"),
		"full_scan":  fullScan,
		"index_used": indexUsed,
		"query":      query,
	}, nil
}

// DBHealthCheckTool checks SQLite database health metrics.
type DBHealthCheckTool struct {
	DB *sql.DB
}

func (t *DBHealthCheckTool) Name() string { return "db_health_check" }
func (t *DBHealthCheckTool) Description() string {
	return "Check database health: integrity, size, table stats, and free space"
}
func (t *DBHealthCheckTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "db_health_check",
		Description: "Run SQLite health checks: integrity verification, page counts, free pages, and per-table row counts.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *DBHealthCheckTool) Execute(_ context.Context, _ map[string]any) (any, error) {
	if t.DB == nil {
		return map[string]any{"error": "database not configured"}, nil
	}

	var integrity string
	_ = t.DB.QueryRow("PRAGMA integrity_check").Scan(&integrity)

	var pageCount, freePages, pageSize int
	_ = t.DB.QueryRow("PRAGMA page_count").Scan(&pageCount)
	_ = t.DB.QueryRow("PRAGMA freelist_count").Scan(&freePages)
	_ = t.DB.QueryRow("PRAGMA page_size").Scan(&pageSize)

	sizeBytes := pageCount * pageSize

	// Collect table names first, close rows before querying counts
	// (avoids deadlock when MaxOpenConns=1 and rows are still open).
	var tableNames []string
	if rows, err := t.DB.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"); err == nil {
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err == nil {
				tableNames = append(tableNames, name)
			}
		}
		_ = rows.Close()
	}
	tables := []map[string]any{}
	for _, name := range tableNames {
		var count int
		_ = t.DB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", name)).Scan(&count)
		tables = append(tables, map[string]any{
			"name":      name,
			"row_count": count,
		})
	}

	return map[string]any{
		"integrity":  integrity,
		"pages":      pageCount,
		"free_pages": freePages,
		"page_size":  pageSize,
		"size_bytes": sizeBytes,
		"tables":     tables,
	}, nil
}

// ---- SchemaInspectTool ------------------------------------------------------

// SchemaInspectTool introspects the SQLite schema for tables, columns, indexes,
// foreign keys, and row counts.
type SchemaInspectTool struct {
	DB *sql.DB
}

func (t *SchemaInspectTool) Name() string { return "schema_inspect" }
func (t *SchemaInspectTool) Description() string {
	return "Inspect SQLite schema: columns, indexes, foreign keys, and row counts per table"
}
func (t *SchemaInspectTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "schema_inspect",
		Description: "Introspect the SQLite database schema. Returns table definitions with column types, nullable flags, defaults, indexes (with their columns), foreign key relationships, and row counts. Optionally filter to a single table.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"table": map[string]any{
					"type":        "string",
					"description": "Specific table name to inspect (optional; all tables if omitted)",
				},
			},
		},
	}
}

func (t *SchemaInspectTool) Execute(_ context.Context, args map[string]any) (any, error) {
	if t.DB == nil {
		return map[string]any{"error": "database not configured"}, nil
	}

	tableFilter, _ := args["table"].(string)

	// 1. Collect table names (close rows before running per-table queries — MaxOpenConns=1)
	var tableNames []string
	{
		query := "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
		if tableFilter != "" {
			query += fmt.Sprintf(" AND name = '%s'", strings.ReplaceAll(tableFilter, "'", "''"))
		}
		query += " ORDER BY name"
		rows, err := t.DB.Query(query)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("list tables: %v", err)}, nil
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err == nil {
				tableNames = append(tableNames, name)
			}
		}
		_ = rows.Close()
	}

	tableList := make([]map[string]any, 0, len(tableNames))
	for _, name := range tableNames {
		tableInfo := map[string]any{"name": name}

		// Row count
		var rowCount int
		_ = t.DB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", name)).Scan(&rowCount)
		tableInfo["row_count"] = rowCount

		// Columns via PRAGMA table_info
		columns := []map[string]any{}
		{
			rows, err := t.DB.Query(fmt.Sprintf("PRAGMA table_info(\"%s\")", name))
			if err == nil {
				for rows.Next() {
					var cid int
					var colName, colType string
					var notNull, pk int
					var dfltValue sql.NullString
					if err := rows.Scan(&cid, &colName, &colType, &notNull, &dfltValue, &pk); err == nil {
						col := map[string]any{
							"cid":         cid,
							"name":        colName,
							"type":        colType,
							"not_null":    notNull == 1,
							"primary_key": pk > 0,
						}
						if dfltValue.Valid {
							col["default"] = dfltValue.String
						}
						columns = append(columns, col)
					}
				}
				_ = rows.Close()
			}
		}
		tableInfo["columns"] = columns

		// Indexes via PRAGMA index_list + PRAGMA index_info
		indexes := []map[string]any{}
		{
			type indexMeta struct {
				name   string
				unique int
			}
			var idxMetas []indexMeta
			rows, err := t.DB.Query(fmt.Sprintf("PRAGMA index_list(\"%s\")", name))
			if err == nil {
				for rows.Next() {
					var seq, unique int
					var idxName, origin string
					var partial int
					if err := rows.Scan(&seq, &idxName, &unique, &origin, &partial); err == nil {
						idxMetas = append(idxMetas, indexMeta{idxName, unique})
					}
				}
				_ = rows.Close()
			}

			for _, im := range idxMetas {
				idxCols := []string{}
				rows2, err := t.DB.Query(fmt.Sprintf("PRAGMA index_info(\"%s\")", im.name))
				if err == nil {
					for rows2.Next() {
						var seqno, cid int
						var colName string
						if err := rows2.Scan(&seqno, &cid, &colName); err == nil {
							idxCols = append(idxCols, colName)
						}
					}
					_ = rows2.Close()
				}
				indexes = append(indexes, map[string]any{
					"name":    im.name,
					"unique":  im.unique == 1,
					"columns": idxCols,
				})
			}
		}
		tableInfo["indexes"] = indexes

		// Foreign keys via PRAGMA foreign_key_list
		foreignKeys := []map[string]any{}
		{
			rows, err := t.DB.Query(fmt.Sprintf("PRAGMA foreign_key_list(\"%s\")", name))
			if err == nil {
				for rows.Next() {
					var id, seq int
					var table, from, to, onUpdate, onDelete, match string
					if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err == nil {
						foreignKeys = append(foreignKeys, map[string]any{
							"from":      from,
							"to_table":  table,
							"to_column": to,
							"on_update": onUpdate,
							"on_delete": onDelete,
						})
					}
				}
				_ = rows.Close()
			}
		}
		tableInfo["foreign_keys"] = foreignKeys

		tableList = append(tableList, tableInfo)
	}

	return map[string]any{
		"tables":      tableList,
		"table_count": len(tableList),
	}, nil
}

// ---- DataProfileTool --------------------------------------------------------

// DataProfileTool computes null rates, distinct counts, and numeric/text
// statistics for each column in a table using a sample of rows.
type DataProfileTool struct {
	DB *sql.DB
}

func (t *DataProfileTool) Name() string { return "data_profile" }
func (t *DataProfileTool) Description() string {
	return "Profile a SQLite table: null rates, distinct counts, and min/max/avg per column on a sample"
}
func (t *DataProfileTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "data_profile",
		Description: "Profile a SQLite table by sampling rows and computing per-column statistics: NULL count, distinct count, and (for numeric/text) min, max, and average value or length.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"table": map[string]any{
					"type":        "string",
					"description": "Table name to profile",
				},
				"sample_size": map[string]any{
					"type":        "integer",
					"description": "Number of rows to sample (default: 1000)",
				},
			},
			"required": []string{"table"},
		},
	}
}

func (t *DataProfileTool) Execute(_ context.Context, args map[string]any) (any, error) {
	if t.DB == nil {
		return map[string]any{"error": "database not configured"}, nil
	}

	tableName, ok := args["table"].(string)
	if !ok || tableName == "" {
		return nil, fmt.Errorf("data_profile: 'table' is required")
	}
	sampleSize := 1000
	if v, ok := args["sample_size"].(float64); ok && v > 0 {
		sampleSize = int(v)
	}

	// Validate table name against sqlite_master to prevent injection
	var validName string
	err := t.DB.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?", tableName,
	).Scan(&validName)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("table not found: %s", tableName)}, nil
	}

	// Get total row count
	var rowCount int
	_ = t.DB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", validName)).Scan(&rowCount)

	// Get column info
	type colMeta struct {
		name    string
		colType string
	}
	var cols []colMeta
	{
		rows, err := t.DB.Query(fmt.Sprintf("PRAGMA table_info(\"%s\")", validName))
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("table_info: %v", err)}, nil
		}
		for rows.Next() {
			var cid, notNull, pk int
			var colName, colType string
			var dflt sql.NullString
			if err := rows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk); err == nil {
				cols = append(cols, colMeta{colName, colType})
			}
		}
		_ = rows.Close()
	}

	actualSample := sampleSize
	if rowCount < actualSample {
		actualSample = rowCount
	}

	// Build per-column aggregate queries; run one query per column to avoid
	// locking issues with MaxOpenConns=1.
	colProfiles := make([]map[string]any, 0, len(cols))
	for _, col := range cols {
		qCol := fmt.Sprintf("\"%s\"", col.name)
		subquery := fmt.Sprintf("(SELECT %s FROM \"%s\" LIMIT %d)", qCol, validName, sampleSize)

		profile := map[string]any{
			"name": col.name,
			"type": col.colType,
		}

		// NULL count and DISTINCT count
		var nullCount, distinctCount int
		_ = t.DB.QueryRow(fmt.Sprintf(
			"SELECT SUM(CASE WHEN v IS NULL THEN 1 ELSE 0 END), COUNT(DISTINCT v) FROM %s AS t(v)", subquery,
		)).Scan(&nullCount, &distinctCount)
		profile["null_count"] = nullCount
		profile["distinct_count"] = distinctCount
		if actualSample > 0 {
			profile["null_percent"] = fmt.Sprintf("%.1f", float64(nullCount)/float64(actualSample)*100)
		} else {
			profile["null_percent"] = "0.0"
		}

		// Type-specific stats
		upperType := strings.ToUpper(col.colType)
		isNumeric := strings.Contains(upperType, "INT") || strings.Contains(upperType, "REAL") ||
			strings.Contains(upperType, "FLOAT") || strings.Contains(upperType, "DOUBLE") ||
			strings.Contains(upperType, "NUMERIC") || strings.Contains(upperType, "DECIMAL") ||
			upperType == "NUMBER"
		isText := strings.Contains(upperType, "TEXT") || strings.Contains(upperType, "CHAR") ||
			strings.Contains(upperType, "CLOB") || strings.Contains(upperType, "VARCHAR") ||
			upperType == "STRING"

		if isNumeric {
			var minVal, maxVal, avgVal sql.NullFloat64
			_ = t.DB.QueryRow(fmt.Sprintf(
				"SELECT MIN(CAST(v AS REAL)), MAX(CAST(v AS REAL)), AVG(CAST(v AS REAL)) FROM %s AS t(v) WHERE v IS NOT NULL", subquery,
			)).Scan(&minVal, &maxVal, &avgVal)
			if minVal.Valid {
				profile["min"] = minVal.Float64
			}
			if maxVal.Valid {
				profile["max"] = maxVal.Float64
			}
			if avgVal.Valid {
				profile["avg"] = avgVal.Float64
			}
		} else if isText {
			var minLen, maxLen sql.NullFloat64
			var avgLen sql.NullFloat64
			_ = t.DB.QueryRow(fmt.Sprintf(
				"SELECT MIN(LENGTH(v)), MAX(LENGTH(v)), AVG(LENGTH(v)) FROM %s AS t(v) WHERE v IS NOT NULL", subquery,
			)).Scan(&minLen, &maxLen, &avgLen)
			if minLen.Valid {
				profile["min"] = int(minLen.Float64)
			}
			if maxLen.Valid {
				profile["max"] = int(maxLen.Float64)
			}
			if avgLen.Valid {
				profile["avg"] = avgLen.Float64
			}
		}

		colProfiles = append(colProfiles, profile)
	}

	return map[string]any{
		"table":       validName,
		"row_count":   rowCount,
		"sample_size": actualSample,
		"columns":     colProfiles,
	}, nil
}
