package orchestrator

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupApprovalDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(createApprovalsTable)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestApprovalCreate(t *testing.T) {
	db := setupApprovalDB(t)
	am := NewApprovalManager(db)
	ctx := context.Background()

	err := am.Create(ctx, Approval{
		ID:     "test-1",
		Action: "delete production database",
		Reason: "cleanup old data",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	a, err := am.Get(ctx, "test-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if a.Status != ApprovalPending {
		t.Errorf("expected pending, got %s", a.Status)
	}
	if a.Action != "delete production database" {
		t.Errorf("expected action, got %s", a.Action)
	}
}

func TestApprovalApprove(t *testing.T) {
	db := setupApprovalDB(t)
	am := NewApprovalManager(db)
	ctx := context.Background()

	err := am.Create(ctx, Approval{ID: "test-2", Action: "push to prod"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := am.Approve(ctx, "test-2", "Looks good"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	a, err := am.Get(ctx, "test-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if a.Status != ApprovalApproved {
		t.Errorf("expected approved, got %s", a.Status)
	}
	if a.ReviewerComment != "Looks good" {
		t.Errorf("expected comment, got %s", a.ReviewerComment)
	}
}

func TestApprovalReject(t *testing.T) {
	db := setupApprovalDB(t)
	am := NewApprovalManager(db)
	ctx := context.Background()

	err := am.Create(ctx, Approval{ID: "test-3", Action: "exec rm -rf"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := am.Reject(ctx, "test-3", "Too dangerous"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	a, err := am.Get(ctx, "test-3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if a.Status != ApprovalRejected {
		t.Errorf("expected rejected, got %s", a.Status)
	}
}

func TestApprovalListPending(t *testing.T) {
	db := setupApprovalDB(t)
	am := NewApprovalManager(db)
	ctx := context.Background()

	for i, action := range []string{"action-a", "action-b", "action-c"} {
		_ = i
		err := am.Create(ctx, Approval{Action: action})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// Approve one
	pending, err := am.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending, got %d", len(pending))
	}

	_ = am.Approve(ctx, pending[0].ID, "ok")
	pending, err = am.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending after approve: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("expected 2 pending after approval, got %d", len(pending))
	}
}

func TestApprovalTimeout(t *testing.T) {
	db := setupApprovalDB(t)
	am := NewApprovalManager(db)
	ctx := context.Background()

	// Insert an approval that is already expired (timeout_minutes = 0 relative to old created_at)
	_, err := db.Exec(
		`INSERT INTO approvals (id, action, status, timeout_minutes, created_at) VALUES (?, ?, 'pending', 1, datetime('now', '-2 minutes'))`,
		"test-timeout", "something",
	)
	if err != nil {
		t.Fatalf("insert expired approval: %v", err)
	}

	if err := am.CheckTimeout(ctx); err != nil {
		t.Fatalf("CheckTimeout: %v", err)
	}

	a, err := am.Get(ctx, "test-timeout")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if a.Status != ApprovalTimeout {
		t.Errorf("expected timeout status, got %s", a.Status)
	}
}

func TestApprovalWaitForResolution(t *testing.T) {
	db := setupApprovalDB(t)
	am := NewApprovalManager(db)
	ctx := context.Background()

	err := am.Create(ctx, Approval{ID: "wait-test", Action: "risky action"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Approve it in a goroutine after a short delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = am.Approve(context.Background(), "wait-test", "approved in test")
	}()

	a, err := am.WaitForResolution(ctx, "wait-test", 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForResolution: %v", err)
	}
	if a.Status != ApprovalApproved {
		t.Errorf("expected approved, got %s", a.Status)
	}
}

func TestApprovalWaitForResolutionTimeout(t *testing.T) {
	db := setupApprovalDB(t)
	am := NewApprovalManager(db)
	ctx := context.Background()

	err := am.Create(ctx, Approval{ID: "timeout-wait", Action: "slow action"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Wait with a very short timeout
	a, err := am.WaitForResolution(ctx, "timeout-wait", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForResolution: %v", err)
	}
	if a.Status != ApprovalTimeout {
		t.Errorf("expected timeout status, got %s", a.Status)
	}
}
