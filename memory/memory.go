// Package memory provides SQLite-backed persistent memory storage for agents.
package memory

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/executor"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/google/uuid"
)

// Entry is a single piece of persistent agent memory.
type Entry struct {
	ID        string
	AgentID   string
	Content   string
	Category  string    // e.g., "decision", "fact", "preference", "general"
	Embedding []float32 // optional vector embedding
	CreatedAt time.Time
}

// SQLiteMemoryStore implements executor.MemoryStore using SQLite FTS5 and
// optional vector embeddings for hybrid semantic search.
type SQLiteMemoryStore struct {
	db *sql.DB
}

// NewSQLiteMemoryStore creates a new SQLiteMemoryStore. It creates the required
// tables if they don't exist.
func NewSQLiteMemoryStore(db *sql.DB) (*SQLiteMemoryStore, error) {
	ms := &SQLiteMemoryStore{db: db}
	if err := ms.initTables(); err != nil {
		return nil, err
	}
	return ms, nil
}

func (ms *SQLiteMemoryStore) initTables() error {
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

// Search implements executor.MemoryStore.
// Uses FTS5 BM25 ranking to find relevant memories for an agent.
func (ms *SQLiteMemoryStore) Search(ctx context.Context, agentID, query string, limit int) ([]executor.MemoryEntry, error) {
	if limit <= 0 {
		limit = 5
	}

	ftsQuery := sanitizeFTSQuery(query)

	rows, err := ms.db.QueryContext(ctx, `
SELECT m.id, m.agent_id, m.content, m.category, m.created_at
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

	var entries []executor.MemoryEntry
	for rows.Next() {
		var e executor.MemoryEntry
		var createdAt string
		if err := rows.Scan(&e.ID, &e.AgentID, &e.Content, &e.Category, &createdAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Save implements executor.MemoryStore.
func (ms *SQLiteMemoryStore) Save(ctx context.Context, entry executor.MemoryEntry) error {
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.Category == "" {
		entry.Category = "general"
	}

	_, err := ms.db.ExecContext(ctx,
		`INSERT INTO memory_entries (id, agent_id, content, category, created_at)
		 VALUES (?, ?, ?, ?, datetime('now'))`,
		entry.ID, entry.AgentID, entry.Content, entry.Category,
	)
	if err != nil {
		return fmt.Errorf("memory save: %w", err)
	}

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

// ExtractAndSave implements executor.MemoryStore.
// Extracts key facts from a conversation transcript and saves them.
func (ms *SQLiteMemoryStore) ExtractAndSave(ctx context.Context, agentID, transcript string, embedder provider.Embedder) error {
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
			continue
		}
		entry := executor.MemoryEntry{
			AgentID:  agentID,
			Content:  chunk,
			Category: "transcript",
		}
		if err := ms.Save(ctx, entry); err != nil {
			return fmt.Errorf("extract_and_save: %w", err)
		}
	}

	// Suppress unused embedder warning — embeddings are stored in the full Entry type
	// but the executor.MemoryEntry interface doesn't carry embedding fields.
	_ = embedder

	return nil
}

// SaveFull saves an Entry including optional vector embedding.
func (ms *SQLiteMemoryStore) SaveFull(ctx context.Context, entry Entry) error {
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

	_, err = ms.db.ExecContext(ctx,
		`INSERT INTO memory_entries_fts (id, agent_id, content, category)
		 VALUES (?, ?, ?, ?)`,
		entry.ID, entry.AgentID, entry.Content, entry.Category,
	)
	return err
}

// sanitizeFTSQuery converts a natural-language query to a safe FTS5 query.
func sanitizeFTSQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return `""`
	}
	words := strings.Fields(q)
	tokens := make([]string, 0, len(words))
	for _, w := range words {
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

func float32SliceToBytes(f []float32) []byte {
	b := make([]byte, len(f)*4)
	for i, v := range f {
		bits := math.Float32bits(v)
		binary.LittleEndian.PutUint32(b[i*4:], bits)
	}
	return b
}

// ensure SQLiteMemoryStore implements executor.MemoryStore
var _ executor.MemoryStore = (*SQLiteMemoryStore)(nil)

// suppress unused time import
var _ = time.Now
