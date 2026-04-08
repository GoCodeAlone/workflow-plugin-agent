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

	if err := ps.Grant("file_read", Allow, "global", "user"); err != nil {
		t.Fatal(err)
	}

	action, ok := ps.Check("file_read", "global")
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

	_, ok := ps.Check("file_read", "global")
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

	_ = ps.Grant("file_read", Allow, "global", "user")
	if err := ps.Revoke("file_read", "global"); err != nil {
		t.Fatal(err)
	}

	_, ok := ps.Check("file_read", "global")
	if ok {
		t.Error("expected no match after revoke")
	}
}

func TestPermissionStoreGlobCheck(t *testing.T) {
	db := testDB(t)
	ps, err := NewPermissionStore(db)
	if err != nil {
		t.Fatal(err)
	}

	_ = ps.Grant("blackboard_*", Allow, "global", "user")

	// Wildcard grant should match specific tools
	action, ok := ps.Check("blackboard_read", "global")
	if !ok {
		t.Fatal("expected glob match for blackboard_read")
	}
	if action != Allow {
		t.Errorf("got %v, want Allow", action)
	}

	action, ok = ps.Check("blackboard_write", "global")
	if !ok {
		t.Fatal("expected glob match for blackboard_write")
	}
	if action != Allow {
		t.Errorf("got %v, want Allow", action)
	}

	// Non-matching tool should not match
	_, ok = ps.Check("file_read", "global")
	if ok {
		t.Error("file_read should not match blackboard_* grant")
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

// TestTrustEngineDataRace verifies there are no data races when TrustEngine methods
// are called concurrently. Run with: go test -race ./policy/
func TestTrustEngineDataRace(t *testing.T) {
	te := NewTrustEngine("conservative", nil, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			te.SetMode("permissive")
			te.SetMode("conservative")
		}
	}()
	for i := 0; i < 100; i++ {
		te.EvaluateCommand("git status")
		te.EvaluatePath("/Users/jon/.ssh/id_rsa")
		te.Mode()
	}
	<-done
}
