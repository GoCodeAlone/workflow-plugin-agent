package policy

import "database/sql"

// PermissionStore persists "always allow/deny" grants across sessions.
// Full implementation is in Task 2.2.
type PermissionStore struct {
	db *sql.DB
}

// NewPermissionStore creates a PermissionStore backed by the given DB.
func NewPermissionStore(db *sql.DB) (*PermissionStore, error) {
	ps := &PermissionStore{db: db}
	if err := ps.init(); err != nil {
		return nil, err
	}
	return ps, nil
}

func (ps *PermissionStore) init() error {
	_, err := ps.db.Exec(`CREATE TABLE IF NOT EXISTS permissions (
		pattern    TEXT NOT NULL,
		action     TEXT NOT NULL,
		scope      TEXT NOT NULL,
		granted_by TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (pattern, scope)
	)`)
	return err
}

// Grant stores a persistent allow/deny for a pattern+scope.
func (ps *PermissionStore) Grant(pattern string, action Action, scope, grantedBy string) error {
	_, err := ps.db.Exec(
		`INSERT OR REPLACE INTO permissions (pattern, action, scope, granted_by) VALUES (?, ?, ?, ?)`,
		pattern, string(action), scope, grantedBy,
	)
	return err
}

// Check returns the stored action for pattern+scope, if any.
func (ps *PermissionStore) Check(pattern, scope string) (Action, bool) {
	var action string
	err := ps.db.QueryRow(
		`SELECT action FROM permissions WHERE pattern = ? AND scope = ?`,
		pattern, scope,
	).Scan(&action)
	if err != nil {
		return Deny, false
	}
	return Action(action), true
}

// Revoke removes a persistent grant.
func (ps *PermissionStore) Revoke(pattern, scope string) error {
	_, err := ps.db.Exec(
		`DELETE FROM permissions WHERE pattern = ? AND scope = ?`,
		pattern, scope,
	)
	return err
}
