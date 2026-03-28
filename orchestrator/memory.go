package orchestrator

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	"github.com/google/uuid"
)

// MemoryEntry is a single piece of persistent agent memory.
type MemoryEntry struct {
	ID        string
	AgentID   string
	Content   string
	Category  string    // e.g., "decision", "fact", "preference", "general"
	Embedding []float32 // optional vector embedding
	CreatedAt time.Time
}

// MemoryStore provides persistent memory storage for agents using SQLite FTS5 and
// optional vector embeddings for hybrid semantic search.
type MemoryStore struct {
	db *sql.DB
}

// NewMemoryStore creates a new MemoryStore backed by the given database.
func NewMemoryStore(db *sql.DB) *MemoryStore {
	return &MemoryStore{db: db}
}

// InitTables creates the memory_entries table and FTS5 virtual table if they don't exist.
func (ms *MemoryStore) InitTables() error {
	// Main storage table with embedding BLOB
	_, err := ms.db.Exec(`
CREATE TABLE IF NOT EXISTS memory_entries (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT 'general',
    embedding BLOB,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`)
	if err != nil {
		return fmt.Errorf("memory_entries table: %w", err)
	}

	// Standalone FTS5 virtual table — populated explicitly in Save to avoid trigger issues.
	_, err = ms.db.Exec(`
CREATE VIRTUAL TABLE IF NOT EXISTS memory_entries_fts USING fts5(
    id UNINDEXED,
    agent_id UNINDEXED,
    content,
    category
);`)
	if err != nil {
		return fmt.Errorf("memory_entries_fts table: %w", err)
	}

	return nil
}

// Save persists a memory entry. If entry.ID is empty, a new UUID is assigned.
func (ms *MemoryStore) Save(ctx context.Context, entry MemoryEntry) error {
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.Category == "" {
		entry.Category = "general"
	}

	var embBlob []byte
	if len(entry.Embedding) > 0 {
		embBlob = float32SliceToBytes(entry.Embedding)
	}

	_, err := ms.db.ExecContext(ctx,
		`INSERT INTO memory_entries (id, agent_id, content, category, embedding, created_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'))`,
		entry.ID, entry.AgentID, entry.Content, entry.Category, embBlob,
	)
	if err != nil {
		return fmt.Errorf("memory save: %w", err)
	}

	// Keep FTS index in sync (explicit insert — avoids trigger compatibility issues)
	_, err = ms.db.ExecContext(ctx,
		`INSERT INTO memory_entries_fts (id, agent_id, content, category)
		 VALUES (?, ?, ?, ?)`,
		entry.ID, entry.AgentID, entry.Content, entry.Category,
	)
	if err != nil {
		return fmt.Errorf("memory save FTS: %w", err)
	}

	return nil
}

// SaveMemory is a convenience method that satisfies tools.MemoryStoreSaver.
func (ms *MemoryStore) SaveMemory(ctx context.Context, agentID, content, category string) error {
	return ms.Save(ctx, MemoryEntry{
		AgentID:  agentID,
		Content:  content,
		Category: category,
	})
}

// SearchMemory satisfies tools.MemoryStoreSearcher, returning lightweight result structs.
func (ms *MemoryStore) SearchMemory(ctx context.Context, agentID, query string, limit int) ([]tools.MemoryEntryResult, error) {
	entries, err := ms.Search(ctx, agentID, query, limit)
	if err != nil {
		return nil, err
	}
	results := make([]tools.MemoryEntryResult, len(entries))
	for i, e := range entries {
		results[i] = tools.MemoryEntryResult{
			ID:        e.ID,
			Content:   e.Content,
			Category:  e.Category,
			CreatedAt: e.CreatedAt,
		}
	}
	return results, nil
}

// Search uses FTS5 BM25 ranking to find relevant memories for an agent.
func (ms *MemoryStore) Search(ctx context.Context, agentID, query string, limit int) ([]MemoryEntry, error) {
	if limit <= 0 {
		limit = 5
	}

	// Sanitize query for FTS5 — wrap in quotes to treat as phrase
	ftsQuery := sanitizeFTSQuery(query)

	// Query FTS5 for matching IDs filtered by agent, then join back for embeddings.
	rows, err := ms.db.QueryContext(ctx, `
SELECT m.id, m.agent_id, m.content, m.category, m.embedding, m.created_at
FROM memory_entries m
WHERE m.id IN (
    SELECT id FROM memory_entries_fts
    WHERE memory_entries_fts MATCH ? AND agent_id = ?
    ORDER BY bm25(memory_entries_fts) ASC
    LIMIT ?
)`,
		ftsQuery, agentID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("memory search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanMemoryRows(rows)
}

// scoredMemoryEntry pairs a MemoryEntry with a combined ranking score.
type scoredMemoryEntry struct {
	entry MemoryEntry
	score float64
}

// SearchHybrid combines 70% cosine similarity + 30% BM25 for hybrid semantic search.
// queryEmbedding is the embedding of the search query. Falls back to FTS-only if nil.
func (ms *MemoryStore) SearchHybrid(ctx context.Context, agentID, query string, queryEmbedding []float32, limit int) ([]MemoryEntry, error) {
	if limit <= 0 {
		limit = 5
	}
	if len(queryEmbedding) == 0 {
		return ms.Search(ctx, agentID, query, limit)
	}

	// Fetch all entries for this agent that have embeddings
	rows, err := ms.db.QueryContext(ctx,
		`SELECT id, agent_id, content, category, embedding, created_at
		 FROM memory_entries WHERE agent_id = ? AND embedding IS NOT NULL`,
		agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("memory hybrid search fetch: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var candidates []scoredMemoryEntry
	for rows.Next() {
		e, err := scanMemoryRow(rows)
		if err != nil {
			continue
		}
		if len(e.Embedding) > 0 {
			sim := cosineSimilarity(queryEmbedding, e.Embedding)
			candidates = append(candidates, scoredMemoryEntry{entry: e, score: float64(sim)})
		}
	}
	_ = rows.Close()

	// Get BM25 scores for FTS candidates
	ftsQuery := sanitizeFTSQuery(query)
	bm25Map := make(map[string]float64)
	ftsRows, ftsErr := ms.db.QueryContext(ctx, `
SELECT id, bm25(memory_entries_fts) AS score
FROM memory_entries_fts
WHERE memory_entries_fts MATCH ? AND agent_id = ?
ORDER BY score ASC
LIMIT ?`,
		ftsQuery, agentID, limit*3,
	)
	if ftsErr == nil {
		defer func() { _ = ftsRows.Close() }()
		type idScore struct {
			id    string
			score float64
		}
		var idScores []idScore
		var rawScores []float64
		for ftsRows.Next() {
			var id string
			var score float64
			if ftsRows.Scan(&id, &score) == nil {
				idScores = append(idScores, idScore{id, score})
				rawScores = append(rawScores, score)
			}
		}
		// Normalize BM25 scores to [0, 1] range (lower BM25 = better match, so invert)
		if len(rawScores) > 0 {
			minS, maxS := rawScores[0], rawScores[0]
			for _, s := range rawScores {
				if s < minS {
					minS = s
				}
				if s > maxS {
					maxS = s
				}
			}
			rng := maxS - minS
			for _, is := range idScores {
				normalized := 0.5
				if rng != 0 {
					normalized = 1.0 - (is.score-minS)/rng
				}
				bm25Map[is.id] = normalized
			}
		}
	}

	// Combine scores: 70% cosine + 30% BM25
	for i := range candidates {
		cosScore := candidates[i].score
		bm25Score := bm25Map[candidates[i].entry.ID]
		candidates[i].score = 0.7*cosScore + 0.3*bm25Score
	}

	// Sort by combined score descending
	sortScoredEntries(candidates)

	// Return top limit results
	results := make([]MemoryEntry, 0, limit)
	for i, c := range candidates {
		if i >= limit {
			break
		}
		results = append(results, c.entry)
	}
	return results, nil
}

// ExtractAndSave extracts key facts from a conversation transcript and saves them.
// If an embedder is provided, embeddings are computed and stored.
func (ms *MemoryStore) ExtractAndSave(ctx context.Context, agentID, transcript string, embedder provider.Embedder) error {
	// Split transcript into chunks (simple line-based extraction)
	lines := strings.Split(transcript, "\n")
	var chunks []string
	var current strings.Builder
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteString(line)
		current.WriteString(" ")
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	for _, chunk := range chunks {
		if len(chunk) < 20 {
			continue // skip very short fragments
		}
		entry := MemoryEntry{
			AgentID:  agentID,
			Content:  chunk,
			Category: "transcript",
		}
		if embedder != nil {
			emb, err := embedder.Embed(ctx, chunk)
			if err == nil {
				entry.Embedding = emb
			}
		}
		if err := ms.Save(ctx, entry); err != nil {
			return fmt.Errorf("extract_and_save: %w", err)
		}
	}
	return nil
}

// scanMemoryRows scans all rows from a query into a []MemoryEntry.
func scanMemoryRows(rows *sql.Rows) ([]MemoryEntry, error) {
	var entries []MemoryEntry
	for rows.Next() {
		e, err := scanMemoryRow(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// scanMemoryRow scans a single row.
func scanMemoryRow(rows *sql.Rows) (MemoryEntry, error) {
	var e MemoryEntry
	var embBlob []byte
	var createdAt string
	err := rows.Scan(&e.ID, &e.AgentID, &e.Content, &e.Category, &embBlob, &createdAt)
	if err != nil {
		return e, err
	}
	if len(embBlob) > 0 {
		e.Embedding = bytesToFloat32Slice(embBlob)
	}
	e.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return e, nil
}

// float32SliceToBytes converts a []float32 to a little-endian byte slice.
func float32SliceToBytes(f []float32) []byte {
	b := make([]byte, len(f)*4)
	for i, v := range f {
		bits := math.Float32bits(v)
		binary.LittleEndian.PutUint32(b[i*4:], bits)
	}
	return b
}

// bytesToFloat32Slice converts a little-endian byte slice back to []float32.
func bytesToFloat32Slice(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	f := make([]float32, len(b)/4)
	for i := range f {
		bits := binary.LittleEndian.Uint32(b[i*4:])
		f[i] = math.Float32frombits(bits)
	}
	return f
}

// cosineSimilarity computes cosine similarity between two vectors.
// Returns 0 if either vector has zero magnitude.
func cosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, normA, normB float64
	for i := 0; i < n; i++ {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}

// sanitizeFTSQuery converts a natural-language query to a safe FTS5 query.
// Each word is an independent token (implicit AND). Special FTS5 characters are stripped.
func sanitizeFTSQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return `""`
	}
	words := strings.Fields(q)
	tokens := make([]string, 0, len(words))
	for _, w := range words {
		// Keep only alphanumeric, hyphen, underscore characters
		cleaned := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '_' {
				return r
			}
			return -1
		}, w)
		if cleaned != "" {
			tokens = append(tokens, cleaned)
		}
	}
	if len(tokens) == 0 {
		return `""`
	}
	return strings.Join(tokens, " ")
}

// sortScoredEntries sorts in-place by score descending (simple insertion sort for small slices).
func sortScoredEntries(entries []scoredMemoryEntry) {
	for i := 1; i < len(entries); i++ {
		key := entries[i]
		j := i - 1
		for j >= 0 && entries[j].score < key.score {
			entries[j+1] = entries[j]
			j--
		}
		entries[j+1] = key
	}
}
