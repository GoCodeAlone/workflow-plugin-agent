package policy

import (
	"database/sql"
	"fmt"
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

// Grant persists an allow/deny decision. Upserts by pattern+scope.
func (ps *PermissionStore) Grant(pattern string, action Action, scope, grantedBy string) error {
	_, _ = ps.db.Exec(
		"DELETE FROM permission_grants WHERE pattern = ? AND scope = ?",
		pattern, scope,
	)
	_, err := ps.db.Exec(
		"INSERT INTO permission_grants (pattern, action, scope, granted_by) VALUES (?, ?, ?, ?)",
		pattern, string(action), scope, grantedBy,
	)
	return err
}

// Revoke removes a persisted grant.
func (ps *PermissionStore) Revoke(pattern, scope string) error {
	_, err := ps.db.Exec(
		"DELETE FROM permission_grants WHERE pattern = ? AND scope = ?",
		pattern, scope,
	)
	return err
}

// Check returns the persisted action for a pattern+scope, if any.
// Checks scope-specific first, then falls back to global.
func (ps *PermissionStore) Check(pattern, scope string) (Action, bool) {
	var actionStr string
	err := ps.db.QueryRow(
		"SELECT action FROM permission_grants WHERE pattern = ? AND scope = ? LIMIT 1",
		pattern, scope,
	).Scan(&actionStr)
	if err == nil {
		return Action(actionStr), true
	}

	// Fall back to global if scope is not global.
	if scope != "global" {
		err = ps.db.QueryRow(
			"SELECT action FROM permission_grants WHERE pattern = ? AND scope = 'global' LIMIT 1",
			pattern,
		).Scan(&actionStr)
		if err == nil {
			return Action(actionStr), true
		}
	}

	return "", false
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
		g.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		grants = append(grants, g)
	}
	return grants, rows.Err()
}
