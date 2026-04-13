package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestExecuteListWithFilter_NoQuery_Summary(t *testing.T) {
	// Build 35 items across two categories to trigger the summary path (>30 lines).
	var items []string
	for i := 0; i < 20; i++ {
		items = append(items, "db_query_"+fmt.Sprintf("%d", i))
	}
	for i := 0; i < 15; i++ {
		items = append(items, "http_request_"+fmt.Sprintf("%d", i))
	}
	fullResult := strings.Join(items, "\n")

	prov := &mockMCPProvider{
		tools:   []string{"list_step_types"},
		results: map[string]any{"list_step_types": fullResult},
	}
	adapter := &inProcessMCPToolAdapter{
		serverName: "wfctl",
		toolName:   "list_step_types",
		provider:   prov,
	}

	result, err := adapter.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	str, ok := result.(string)
	if !ok {
		t.Fatalf("Execute() result type = %T, want string", result)
	}

	if !strings.Contains(str, "items available") {
		t.Errorf("expected summary with item count, got: %s", str)
	}
	if !strings.Contains(str, `"query"`) {
		t.Errorf("expected query hint in summary, got: %s", str)
	}
}

func TestExecuteListWithFilter_WithQuery_FilteredResults(t *testing.T) {
	fullResult := "db_query\ndb_insert\nhttp_request\nhttp_get\nfile_write"
	prov := &mockMCPProvider{
		tools:   []string{"list_step_types"},
		results: map[string]any{"list_step_types": fullResult},
	}
	adapter := &inProcessMCPToolAdapter{
		serverName: "wfctl",
		toolName:   "list_step_types",
		provider:   prov,
	}

	result, err := adapter.Execute(context.Background(), map[string]any{"query": "db"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	str, _ := result.(string)
	if !strings.Contains(str, "db_query") {
		t.Errorf("expected db_query in filtered results, got: %s", str)
	}
	if strings.Contains(str, "http_request") {
		t.Errorf("expected http_request to be filtered out, got: %s", str)
	}
}

func TestExecuteListWithFilter_WithQuery_NoMatches(t *testing.T) {
	fullResult := "db_query\ndb_insert\nhttp_request"
	prov := &mockMCPProvider{
		tools:   []string{"list_step_types"},
		results: map[string]any{"list_step_types": fullResult},
	}
	adapter := &inProcessMCPToolAdapter{
		serverName: "wfctl",
		toolName:   "list_step_types",
		provider:   prov,
	}

	result, err := adapter.Execute(context.Background(), map[string]any{"query": "xyz_nonexistent"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	str, _ := result.(string)
	if strings.Contains(str, "db_query") {
		t.Errorf("expected no results for non-matching query, got: %s", str)
	}
}

func TestExecuteListWithFilter_SmallList_PassThrough(t *testing.T) {
	// Fewer than 30 items — should pass through without summary.
	fullResult := "db_query\nhttp_request\nfile_write"
	prov := &mockMCPProvider{
		tools:   []string{"list_step_types"},
		results: map[string]any{"list_step_types": fullResult},
	}
	adapter := &inProcessMCPToolAdapter{
		serverName: "wfctl",
		toolName:   "list_step_types",
		provider:   prov,
	}

	result, err := adapter.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result != fullResult {
		t.Errorf("expected pass-through for small list, got: %v", result)
	}
}

func TestExecuteListWithFilter_NonListTool_Unchanged(t *testing.T) {
	prov := &mockMCPProvider{
		tools:   []string{"validate_config"},
		results: map[string]any{"validate_config": "ok"},
	}
	adapter := &inProcessMCPToolAdapter{
		serverName: "wfctl",
		toolName:   "validate_config",
		provider:   prov,
	}

	result, err := adapter.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result != "ok" {
		t.Errorf("expected direct pass-through for non-list tool, got: %v", result)
	}
}
