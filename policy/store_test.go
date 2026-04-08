package policy

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPermissionStoreInit(t *testing.T) {
	db := testDB(t)
	ps, err := NewPermissionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if ps == nil {
		t.Fatal("NewPermissionStore returned nil")
	}
}

func TestPermissionStoreGrantAndCheck(t *testing.T) {
	db := testDB(t)
	ps, err := NewPermissionStore(db)
	if err != nil {
		t.Fatal(err)
	}

	if err := ps.Grant("bash:git *", Allow, "global", "user"); err != nil {
		t.Fatal(err)
	}

	action, ok := ps.Check("bash:git *", "global")
	if !ok {
		t.Fatal("expected match")
	}
	if action != Allow {
		t.Errorf("got %v, want Allow", action)
	}
}

func TestPermissionStoreNoMatch(t *testing.T) {
	db := testDB(t)
	ps, err := NewPermissionStore(db)
	if err != nil {
		t.Fatal(err)
	}

	_, ok := ps.Check("bash:git *", "global")
	if ok {
		t.Error("expected no match")
	}
}

func TestPermissionStoreRevoke(t *testing.T) {
	db := testDB(t)
	ps, err := NewPermissionStore(db)
	if err != nil {
		t.Fatal(err)
	}

	_ = ps.Grant("bash:git *", Allow, "global", "user")
	if err := ps.Revoke("bash:git *", "global"); err != nil {
		t.Fatal(err)
	}

	_, ok := ps.Check("bash:git *", "global")
	if ok {
		t.Error("expected no match after revoke")
	}
}

func TestPermissionStoreList(t *testing.T) {
	db := testDB(t)
	ps, err := NewPermissionStore(db)
	if err != nil {
		t.Fatal(err)
	}

	_ = ps.Grant("file_read", Allow, "global", "user")
	_ = ps.Grant("bash:rm *", Deny, "global", "config")

	grants, err := ps.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 {
		t.Fatalf("expected 2 grants, got %d", len(grants))
	}
}

func TestPermissionStoreScopedCheck(t *testing.T) {
	db := testDB(t)
	ps, err := NewPermissionStore(db)
	if err != nil {
		t.Fatal(err)
	}

	_ = ps.Grant("file_write", Deny, "agent:coder", "user")
	_ = ps.Grant("file_write", Allow, "global", "config")

	// Scoped check should find the agent-specific deny
	action, ok := ps.Check("file_write", "agent:coder")
	if !ok {
		t.Fatal("expected match for agent scope")
	}
	if action != Deny {
		t.Errorf("got %v, want Deny for agent scope", action)
	}

	// Global scope should find the allow
	action, ok = ps.Check("file_write", "global")
	if !ok {
		t.Fatal("expected match for global scope")
	}
	if action != Allow {
		t.Errorf("got %v, want Allow for global scope", action)
	}
}
