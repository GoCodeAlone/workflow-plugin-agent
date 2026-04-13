package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Artifact is a structured piece of data posted to the Blackboard by a pipeline phase.
type Artifact struct {
	ID        string
	Phase     string         // "design", "implement", "review", "security", "approve"
	AgentID   string
	Type      string         // "config_diff", "validation_report", "iac_plan", "review_findings", "approval_decision", "yaml_config"
	Content   map[string]any
	Tags      []string
	CreatedAt time.Time
}

// Blackboard is a SQLite-backed shared artifact exchange for pipeline phases.
// Subscribers can watch for new artifacts via channels returned by Subscribe.
type Blackboard struct {
	db     *sql.DB
	sseHub *SSEHub

	mu          sync.RWMutex
	subscribers map[string][]chan Artifact // keyed by phase ("" = all phases)
}

// NewBlackboard creates a Blackboard backed by db and optionally broadcasting to sseHub.
func NewBlackboard(db *sql.DB, sseHub *SSEHub) *Blackboard {
	return &Blackboard{
		db:          db,
		sseHub:      sseHub,
		subscribers: make(map[string][]chan Artifact),
	}
}

// Migrate creates the blackboard_artifacts table if it doesn't exist.
func (b *Blackboard) Migrate(ctx context.Context) error {
	_, err := b.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS blackboard_artifacts (
    id          TEXT PRIMARY KEY,
    phase       TEXT NOT NULL,
    agent_id    TEXT NOT NULL DEFAULT '',
    type        TEXT NOT NULL,
    content     TEXT NOT NULL DEFAULT '{}',
    tags        TEXT NOT NULL DEFAULT '[]',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);`)
	if err != nil {
		return fmt.Errorf("blackboard migrate: %w", err)
	}
	_, err = b.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_blackboard_phase ON blackboard_artifacts(phase);`)
	if err != nil {
		return fmt.Errorf("blackboard migrate index: %w", err)
	}
	return nil
}

// Post inserts an artifact, notifies subscribers, and optionally broadcasts an SSE event.
func (b *Blackboard) Post(ctx context.Context, artifact Artifact) error {
	if artifact.ID == "" {
		artifact.ID = uuid.New().String()
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now()
	}

	contentJSON, err := json.Marshal(artifact.Content)
	if err != nil {
		return fmt.Errorf("blackboard post: marshal content: %w", err)
	}
	tagsJSON, err := json.Marshal(artifact.Tags)
	if err != nil {
		return fmt.Errorf("blackboard post: marshal tags: %w", err)
	}

	_, err = b.db.ExecContext(ctx,
		`INSERT INTO blackboard_artifacts (id, phase, agent_id, type, content, tags, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		artifact.ID, artifact.Phase, artifact.AgentID, artifact.Type,
		string(contentJSON), string(tagsJSON),
		artifact.CreatedAt.UTC().Format("2006-01-02 15:04:05.999999999"),
	)
	if err != nil {
		return fmt.Errorf("blackboard post: %w", err)
	}

	// Notify in-process subscribers
	b.notify(artifact)

	// Broadcast SSE event
	if b.sseHub != nil {
		data, _ := json.Marshal(map[string]any{
			"id":      artifact.ID,
			"phase":   artifact.Phase,
			"type":    artifact.Type,
			"tags":    artifact.Tags,
			"agent_id": artifact.AgentID,
		})
		b.sseHub.BroadcastEvent("blackboard_artifact", string(data))
	}

	return nil
}

// Read returns all artifacts matching the given phase and artifact type.
// Pass an empty string for either field to skip that filter.
func (b *Blackboard) Read(ctx context.Context, phase, artifactType string) ([]Artifact, error) {
	query := `SELECT id, phase, agent_id, type, content, tags, created_at FROM blackboard_artifacts WHERE 1=1`
	args := []any{}

	if phase != "" {
		query += " AND phase = ?"
		args = append(args, phase)
	}
	if artifactType != "" {
		query += " AND type = ?"
		args = append(args, artifactType)
	}
	query += " ORDER BY created_at ASC"

	rows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("blackboard read: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var artifacts []Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}

// ReadLatest returns the most recently posted artifact for the given phase.
// Returns nil, nil if no artifact exists for that phase.
func (b *Blackboard) ReadLatest(ctx context.Context, phase string) (*Artifact, error) {
	row := b.db.QueryRowContext(ctx,
		`SELECT id, phase, agent_id, type, content, tags, created_at
		 FROM blackboard_artifacts WHERE phase = ? ORDER BY created_at DESC LIMIT 1`,
		phase,
	)
	a, err := scanArtifactRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("blackboard read latest: %w", err)
	}
	return a, nil
}

// Subscribe returns a channel that receives new artifacts posted to the given phase.
// Pass "" to receive all artifacts regardless of phase.
// The channel is buffered (64). It is closed when ctx is done.
func (b *Blackboard) Subscribe(ctx context.Context, phase string) <-chan Artifact {
	ch := make(chan Artifact, 64)

	b.mu.Lock()
	b.subscribers[phase] = append(b.subscribers[phase], ch)
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		chans := b.subscribers[phase]
		for i, c := range chans {
			if c == ch {
				b.subscribers[phase] = append(chans[:i], chans[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		close(ch)
	}()

	return ch
}

// notify delivers an artifact to all matching in-process subscribers.
func (b *Blackboard) notify(a Artifact) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	// Phase-specific subscribers
	for _, ch := range b.subscribers[a.Phase] {
		select {
		case ch <- a:
		default:
		}
	}

	// Wildcard subscribers ("" = all phases)
	if a.Phase != "" {
		for _, ch := range b.subscribers[""] {
			select {
			case ch <- a:
			default:
			}
		}
	}
}

// scanArtifact scans a *sql.Rows row into an Artifact.
func scanArtifact(rows *sql.Rows) (Artifact, error) {
	var a Artifact
	var contentJSON, tagsJSON, createdAt string
	err := rows.Scan(&a.ID, &a.Phase, &a.AgentID, &a.Type, &contentJSON, &tagsJSON, &createdAt)
	if err != nil {
		return Artifact{}, fmt.Errorf("scan artifact: %w", err)
	}
	_ = json.Unmarshal([]byte(contentJSON), &a.Content)
	_ = json.Unmarshal([]byte(tagsJSON), &a.Tags)
	a.CreatedAt = parseArtifactTime(createdAt)
	return a, nil
}

// scanArtifactRow scans a *sql.Row into an Artifact.
func scanArtifactRow(row *sql.Row) (*Artifact, error) {
	var a Artifact
	var contentJSON, tagsJSON, createdAt string
	err := row.Scan(&a.ID, &a.Phase, &a.AgentID, &a.Type, &contentJSON, &tagsJSON, &createdAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(contentJSON), &a.Content)
	_ = json.Unmarshal([]byte(tagsJSON), &a.Tags)
	a.CreatedAt = parseArtifactTime(createdAt)
	return &a, nil
}

// parseArtifactTime parses a stored timestamp string, trying sub-second precision first
// then falling back to second-only format.
func parseArtifactTime(s string) time.Time {
	if t, err := time.Parse("2006-01-02 15:04:05.999999999", s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t
	}
	return time.Time{}
}
