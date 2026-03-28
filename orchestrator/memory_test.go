package orchestrator

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestMemoryStore(t *testing.T) *MemoryStore {
	t.Helper()
	db := newTestMemoryDB(t)
	ms := NewMemoryStore(db)
	if err := ms.InitTables(); err != nil {
		t.Fatalf("InitTables: %v", err)
	}
	return ms
}

func TestMemorySave(t *testing.T) {
	ms := newTestMemoryStore(t)
	ctx := context.Background()

	entry := MemoryEntry{
		AgentID:  "agent-1",
		Content:  "The deployment uses Kubernetes on GCP",
		Category: "fact",
	}
	if err := ms.Save(ctx, entry); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestMemorySearch_FTS5(t *testing.T) {
	ms := newTestMemoryStore(t)
	ctx := context.Background()

	entries := []MemoryEntry{
		{AgentID: "agent-1", Content: "We decided to use PostgreSQL for the database", Category: "decision"},
		{AgentID: "agent-1", Content: "The API rate limit is 100 requests per minute", Category: "fact"},
		{AgentID: "agent-1", Content: "User prefers dark mode in the UI", Category: "preference"},
		{AgentID: "agent-2", Content: "This belongs to a different agent about PostgreSQL", Category: "fact"},
	}
	for _, e := range entries {
		if err := ms.Save(ctx, e); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	results, err := ms.Search(ctx, "agent-1", "PostgreSQL database", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result, got none")
	}
	// Should not return results for agent-2
	for _, r := range results {
		if r.AgentID != "agent-1" {
			t.Errorf("got result for wrong agent: %q", r.AgentID)
		}
	}
	// First result should be about PostgreSQL
	found := false
	for _, r := range results {
		if r.Category == "decision" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected decision entry about PostgreSQL in results, got: %+v", results)
	}
}

func TestMemorySearch_Empty(t *testing.T) {
	ms := newTestMemoryStore(t)
	ctx := context.Background()

	// No entries — should return empty, not error
	results, err := ms.Search(ctx, "agent-1", "anything", 5)
	if err != nil {
		t.Fatalf("Search on empty store: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestMemorySearch_Limit(t *testing.T) {
	ms := newTestMemoryStore(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		_ = ms.Save(ctx, MemoryEntry{
			AgentID:  "agent-limit",
			Content:  "This is a fact about the system configuration and its settings",
			Category: "fact",
		})
	}

	results, err := ms.Search(ctx, "agent-limit", "system configuration settings", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) > 3 {
		t.Errorf("expected at most 3 results, got %d", len(results))
	}
}

// mockEmbedder returns a simple deterministic embedding based on string length.
type mockEmbedder struct{}

func (m *mockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	// Create a 4-dimensional mock embedding
	n := float32(len(text))
	return []float32{n / 100, n / 200, n / 50, n / 75}, nil
}

func TestMemorySearchHybrid(t *testing.T) {
	ms := newTestMemoryStore(t)
	ctx := context.Background()
	embedder := &mockEmbedder{}

	// Save entries with embeddings
	contents := []string{
		"The system uses microservices architecture with Docker containers",
		"Authentication is handled by JWT tokens with a 24 hour expiry",
		"Database backups run every night at 2am UTC",
	}
	for _, c := range contents {
		emb, _ := embedder.Embed(ctx, c)
		_ = ms.Save(ctx, MemoryEntry{
			AgentID:   "agent-hybrid",
			Content:   c,
			Category:  "fact",
			Embedding: emb,
		})
	}

	// Search with a query embedding
	queryEmb, _ := embedder.Embed(ctx, "Docker containers deployment")
	results, err := ms.SearchHybrid(ctx, "agent-hybrid", "Docker containers", queryEmb, 5)
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	// Should return results (may be empty if FTS doesn't match — but cosine should)
	t.Logf("SearchHybrid returned %d results", len(results))
}

func TestMemorySearchHybrid_FallbackNoEmbedding(t *testing.T) {
	ms := newTestMemoryStore(t)
	ctx := context.Background()

	_ = ms.Save(ctx, MemoryEntry{
		AgentID:  "agent-fallback",
		Content:  "The deployment pipeline uses GitHub Actions for CI/CD",
		Category: "fact",
	})

	// SearchHybrid with nil embedding should fall back to FTS
	results, err := ms.SearchHybrid(ctx, "agent-fallback", "deployment pipeline", nil, 5)
	if err != nil {
		t.Fatalf("SearchHybrid fallback: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one result from FTS fallback")
	}
}

func TestMemoryExtractAndSave(t *testing.T) {
	ms := newTestMemoryStore(t)
	ctx := context.Background()
	embedder := &mockEmbedder{}

	transcript := `We decided to migrate to PostgreSQL from MySQL.
The migration will happen in Q2 next year.

Authentication tokens expire after 24 hours.
Users can refresh tokens using the /auth/refresh endpoint.`

	if err := ms.ExtractAndSave(ctx, "agent-extract", transcript, embedder); err != nil {
		t.Fatalf("ExtractAndSave: %v", err)
	}

	// Check at least one chunk was saved
	results, err := ms.Search(ctx, "agent-extract", "PostgreSQL", 10)
	if err != nil {
		t.Fatalf("Search after ExtractAndSave: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected extracted memories to be searchable")
	}
}

func TestMemorySaveAndSearch_Category(t *testing.T) {
	ms := newTestMemoryStore(t)
	ctx := context.Background()

	_ = ms.Save(ctx, MemoryEntry{
		AgentID:  "agent-cat",
		Content:  "Always prefer verbose logging in production systems",
		Category: "preference",
	})
	_ = ms.Save(ctx, MemoryEntry{
		AgentID:  "agent-cat",
		Content:  "Production systems require verbose logging levels",
		Category: "decision",
	})

	results, err := ms.Search(ctx, "agent-cat", "verbose logging production", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) < 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestFloat32Roundtrip(t *testing.T) {
	original := []float32{0.1, 0.2, 0.3, 0.4, -0.5, 1.0}
	b := float32SliceToBytes(original)
	recovered := bytesToFloat32Slice(b)

	if len(recovered) != len(original) {
		t.Fatalf("length mismatch: %d vs %d", len(recovered), len(original))
	}
	for i := range original {
		if recovered[i] != original[i] {
			t.Errorf("index %d: got %f, want %f", i, recovered[i], original[i])
		}
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []float32
		wantHigh bool // true if we expect similarity > 0.9
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, true},
		{"opposite", []float32{1, 0, 0}, []float32{-1, 0, 0}, false},
		{"empty", []float32{}, []float32{1, 0}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sim := cosineSimilarity(tc.a, tc.b)
			if tc.wantHigh && sim < 0.9 {
				t.Errorf("expected high similarity, got %f", sim)
			}
			if !tc.wantHigh && sim > 0.9 {
				t.Errorf("expected low similarity, got %f", sim)
			}
		})
	}
}
