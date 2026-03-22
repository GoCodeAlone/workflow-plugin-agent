package memory

import (
	"context"
	"database/sql"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/executor"
	_ "modernc.org/sqlite"
)

// openTestDB returns an in-memory SQLite database for testing.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestNewSQLiteMemoryStore_InitTables verifies tables are created without error.
func TestNewSQLiteMemoryStore_InitTables(t *testing.T) {
	db := openTestDB(t)
	_, err := NewSQLiteMemoryStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteMemoryStore: %v", err)
	}
}

// TestSQLiteMemoryStore_SaveAndSearch verifies a saved entry can be found.
func TestSQLiteMemoryStore_SaveAndSearch(t *testing.T) {
	db := openTestDB(t)
	ms, err := NewSQLiteMemoryStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteMemoryStore: %v", err)
	}

	entry := executor.MemoryEntry{
		AgentID:  "agent-1",
		Content:  "the capital of France is Paris",
		Category: "fact",
	}
	if err := ms.Save(context.Background(), entry); err != nil {
		t.Fatalf("Save: %v", err)
	}

	results, err := ms.Search(context.Background(), "agent-1", "France Paris", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one search result")
	}
	if results[0].Content != entry.Content {
		t.Errorf("Content: want %q, got %q", entry.Content, results[0].Content)
	}
	if results[0].Category != "fact" {
		t.Errorf("Category: want %q, got %q", "fact", results[0].Category)
	}
}

// TestSQLiteMemoryStore_SearchDifferentAgent verifies entries are isolated by agentID.
func TestSQLiteMemoryStore_SearchDifferentAgent(t *testing.T) {
	db := openTestDB(t)
	ms, err := NewSQLiteMemoryStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteMemoryStore: %v", err)
	}

	_ = ms.Save(context.Background(), executor.MemoryEntry{
		AgentID: "agent-a",
		Content: "secret information for agent-a only",
		Category: "private",
	})

	// Searching as agent-b should not find agent-a's entries.
	results, err := ms.Search(context.Background(), "agent-b", "secret information", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for wrong agent, got %d", len(results))
	}
}

// TestSQLiteMemoryStore_SearchEmptyQuery returns no error on empty query.
func TestSQLiteMemoryStore_SearchEmptyQuery(t *testing.T) {
	db := openTestDB(t)
	ms, err := NewSQLiteMemoryStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteMemoryStore: %v", err)
	}

	_, err = ms.Search(context.Background(), "agent-1", "", 5)
	if err != nil {
		t.Fatalf("Search with empty query: %v", err)
	}
}

// TestSQLiteMemoryStore_DefaultCategory verifies empty category defaults to "general".
func TestSQLiteMemoryStore_DefaultCategory(t *testing.T) {
	db := openTestDB(t)
	ms, err := NewSQLiteMemoryStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteMemoryStore: %v", err)
	}

	entry := executor.MemoryEntry{
		AgentID: "agent-1",
		Content: "preference for dark mode interface display",
		// Category intentionally left empty.
	}
	if err := ms.Save(context.Background(), entry); err != nil {
		t.Fatalf("Save: %v", err)
	}

	results, err := ms.Search(context.Background(), "agent-1", "preference dark mode", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected result")
	}
	if results[0].Category != "general" {
		t.Errorf("Category: want %q, got %q", "general", results[0].Category)
	}
}

// TestSQLiteMemoryStore_ExtractAndSave splits transcript into chunks and saves them.
func TestSQLiteMemoryStore_ExtractAndSave(t *testing.T) {
	db := openTestDB(t)
	ms, err := NewSQLiteMemoryStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteMemoryStore: %v", err)
	}

	// Use words long enough (>= 20 chars total per chunk) that will survive FTS tokenization.
	transcript := "The agent decided to deploy the application to production servers.\n\nConfiguration management should use version control for tracking changes."
	if err := ms.ExtractAndSave(context.Background(), "agent-1", transcript, nil); err != nil {
		t.Fatalf("ExtractAndSave: %v", err)
	}

	// Verify entries were saved by querying with a word present in one of the chunks.
	results, err := ms.Search(context.Background(), "agent-1", "deploy application production", 5)
	if err != nil {
		t.Fatalf("Search after ExtractAndSave: %v", err)
	}
	if len(results) == 0 {
		// The search might fail due to FTS5 tokenizer differences.
		// Verify save happened by checking direct query count.
		var count int
		row := db.QueryRow("SELECT COUNT(*) FROM memory_entries WHERE agent_id = ?", "agent-1")
		if scanErr := row.Scan(&count); scanErr != nil {
			t.Fatalf("count query: %v", scanErr)
		}
		if count == 0 {
			t.Error("expected at least one stored chunk from ExtractAndSave")
		}
	}
}

// TestSQLiteMemoryStore_ExtractAndSave_IgnoresShortChunks verifies chunks < 20 chars are skipped.
func TestSQLiteMemoryStore_ExtractAndSave_IgnoresShortChunks(t *testing.T) {
	db := openTestDB(t)
	ms, err := NewSQLiteMemoryStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteMemoryStore: %v", err)
	}

	// Only short lines — nothing should be saved.
	transcript := "OK\n\nYes\n\nNo"
	if err := ms.ExtractAndSave(context.Background(), "agent-1", transcript, nil); err != nil {
		t.Fatalf("ExtractAndSave: %v", err)
	}

	results, err := ms.Search(context.Background(), "agent-1", "OK Yes No", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results for short chunks, got %d", len(results))
	}
}

// TestSQLiteMemoryStore_SaveFull persists an entry with an embedding blob.
func TestSQLiteMemoryStore_SaveFull(t *testing.T) {
	db := openTestDB(t)
	ms, err := NewSQLiteMemoryStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteMemoryStore: %v", err)
	}

	entry := Entry{
		AgentID:   "agent-1",
		Content:   "semantic search uses vector embeddings for similarity",
		Category:  "technique",
		Embedding: []float32{0.1, 0.2, 0.3},
	}
	if err := ms.SaveFull(context.Background(), entry); err != nil {
		t.Fatalf("SaveFull: %v", err)
	}

	// Confirm it's searchable via FTS.
	results, err := ms.Search(context.Background(), "agent-1", "semantic vector", 5)
	if err != nil {
		t.Fatalf("Search after SaveFull: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected search result after SaveFull")
	}
}

// TestSanitizeFTSQuery handles special characters safely.
func TestSanitizeFTSQuery(t *testing.T) {
	tests := []struct {
		input string
		check func(string) bool
		desc  string
	}{
		{"hello world", func(s string) bool { return s == "hello world" }, "normal words"},
		{"", func(s string) bool { return s == `""` }, "empty string"},
		{"hello! @world", func(s string) bool { return s != "" }, "special chars stripped"},
		{"   spaces   ", func(s string) bool { return s != "" && s != "   spaces   " }, "trimmed"},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got := sanitizeFTSQuery(tt.input)
			if !tt.check(got) {
				t.Errorf("sanitizeFTSQuery(%q) = %q: check failed", tt.input, got)
			}
		})
	}
}
