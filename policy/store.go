package policy

import (
	"database/sql"
	"fmt"
	"log"
	"time"
)

const createPermissionGrantsTable = `
CREATE TABLE IF NOT EXISTS permission_grants (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pattern TEXT NOT NULL,
    action TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT 'global',
    granted_by TEXT NOT NULL DEFAULT 'user',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_permission_grants_pattern ON permission_grants(pattern, scope);
`

// PermissionGrant is a persisted trust decision.
type PermissionGrant struct {
	ID        int64
	Pattern   string
	Action    Action
	Scope     string
	GrantedBy string
	CreatedAt time.Time
}

// PermissionStore persists "always allow/deny" trust decisions in SQLite.
type PermissionStore struct {
	db *sql.DB
}

// NewPermissionStore creates a PermissionStore and initializes the table.
func NewPermissionStore(db *sql.DB) (*PermissionStore, error) {
	if _, err := db.Exec(createPermissionGrantsTable); err != nil {
		return nil, fmt.Errorf("permission store: init table: %w", err)
	}
	return &PermissionStore{db: db}, nil
}

// Grant persists an allow/deny decision. Upserts by pattern+scope atomically.
func (ps *PermissionStore) Grant(pattern string, action Action, scope, grantedBy string) error {
	tx, err := ps.db.Begin()
	if err != nil {
		return fmt.Errorf("permission store: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		"DELETE FROM permission_grants WHERE pattern = ? AND scope = ?",
		pattern, scope,
	); err != nil {
		return fmt.Errorf("permission store: delete existing grant: %w", err)
	}
	if _, err := tx.Exec(
		"INSERT INTO permission_grants (pattern, action, scope, granted_by) VALUES (?, ?, ?, ?)",
		pattern, string(action), scope, grantedBy,
	); err != nil {
		return fmt.Errorf("permission store: insert grant: %w", err)
	}
	return tx.Commit()
}

// Revoke removes a persisted grant.
func (ps *PermissionStore) Revoke(pattern, scope string) error {
	_, err := ps.db.Exec(
		"DELETE FROM permission_grants WHERE pattern = ? AND scope = ?",
		pattern, scope,
	)
	return err
}

// Check returns the persisted action for a toolName+scope using glob matching.
// Stored patterns (e.g. "blackboard_*") are matched against the incoming toolName
// using the same logic as TrustEngine rule evaluation. Deny wins across all matches.
// Scope-specific grants are checked alongside global grants.
func (ps *PermissionStore) Check(toolName, scope string) (Action, bool) {
	query := `SELECT pattern, action FROM permission_grants WHERE scope = ? OR scope = 'global'`
	rows, err := ps.db.Query(query, scope)
	if err != nil {
		log.Printf("permission store: check query: %v", err)
		return "", false
	}
	defer func() { _ = rows.Close() }()

	var matched []Action
	for rows.Next() {
		var storedPattern, actionStr string
		if err := rows.Scan(&storedPattern, &actionStr); err != nil {
			continue
		}
		if matchToolPattern(storedPattern, toolName) ||
			matchCommandPattern(storedPattern, toolName) ||
			matchPathPattern(storedPattern, toolName) {
			matched = append(matched, Action(actionStr))
		}
	}
	if rows.Err() != nil || len(matched) == 0 {
		return "", false
	}

	// Deny wins
	for _, a := range matched {
		if a == Deny {
			return Deny, true
		}
	}
	for _, a := range matched {
		if a == Allow {
			return Allow, true
		}
	}
	return Ask, true
}

// List returns all persisted grants.
func (ps *PermissionStore) List() ([]PermissionGrant, error) {
	rows, err := ps.db.Query(
		"SELECT id, pattern, action, scope, granted_by, created_at FROM permission_grants ORDER BY created_at ASC",
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var grants []PermissionGrant
	for rows.Next() {
		var g PermissionGrant
		var createdAt string
		if err := rows.Scan(&g.ID, &g.Pattern, &g.Action, &g.Scope, &g.GrantedBy, &createdAt); err != nil {
			return nil, err
		}
		if g.CreatedAt, err = parseCreatedAt(createdAt); err != nil {
			return nil, fmt.Errorf("permission store: parse created_at %q: %w", createdAt, err)
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// parseCreatedAt parses SQLite datetime strings in multiple formats.
func parseCreatedAt(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized datetime format: %q", s)
}
