package orchestrator

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupHumanRequestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(createHumanRequestsTable)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestHumanRequestCreate(t *testing.T) {
	db := setupHumanRequestDB(t)
	mgr := NewHumanRequestManager(db)
	ctx := context.Background()

	err := mgr.Create(ctx, HumanRequest{
		ID:          "req-1",
		AgentID:     "agent-1",
		TaskID:      "task-1",
		RequestType: RequestTypeToken,
		Title:       "Need GitHub PAT",
		Description: "Need a PAT with repo scope for cloning private repos",
		Urgency:     "high",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	r, err := mgr.Get(ctx, "req-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if r.Status != RequestPending {
		t.Errorf("expected pending, got %s", r.Status)
	}
	if r.RequestType != RequestTypeToken {
		t.Errorf("expected token, got %s", r.RequestType)
	}
	if r.Title != "Need GitHub PAT" {
		t.Errorf("expected title, got %s", r.Title)
	}
	if r.Urgency != "high" {
		t.Errorf("expected high urgency, got %s", r.Urgency)
	}
}

func TestHumanRequestResolve(t *testing.T) {
	db := setupHumanRequestDB(t)
	mgr := NewHumanRequestManager(db)
	ctx := context.Background()

	err := mgr.Create(ctx, HumanRequest{ID: "req-2", Title: "Need API key", RequestType: RequestTypeToken})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := mgr.Resolve(ctx, "req-2", `{"value":"sk-test-123"}`, "Here's your key", "jon"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	r, err := mgr.Get(ctx, "req-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if r.Status != RequestResolved {
		t.Errorf("expected resolved, got %s", r.Status)
	}
	if r.ResponseData != `{"value":"sk-test-123"}` {
		t.Errorf("expected response data, got %s", r.ResponseData)
	}
	if r.ResolvedBy != "jon" {
		t.Errorf("expected resolved_by jon, got %s", r.ResolvedBy)
	}
}

func TestHumanRequestCancel(t *testing.T) {
	db := setupHumanRequestDB(t)
	mgr := NewHumanRequestManager(db)
	ctx := context.Background()

	err := mgr.Create(ctx, HumanRequest{ID: "req-3", Title: "Install kubectl"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := mgr.Cancel(ctx, "req-3", "No longer needed"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	r, err := mgr.Get(ctx, "req-3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if r.Status != RequestCancelled {
		t.Errorf("expected cancelled, got %s", r.Status)
	}
}

func TestHumanRequestListPending(t *testing.T) {
	db := setupHumanRequestDB(t)
	mgr := NewHumanRequestManager(db)
	ctx := context.Background()

	// Create 3 requests with different urgencies
	for _, req := range []HumanRequest{
		{Title: "Low priority", Urgency: "low", RequestType: RequestTypeInfo},
		{Title: "Critical item", Urgency: "critical", RequestType: RequestTypeToken},
		{Title: "Normal request", Urgency: "normal", RequestType: RequestTypeBinary},
	} {
		if err := mgr.Create(ctx, req); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	pending, err := mgr.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending, got %d", len(pending))
	}

	// Critical should be first due to urgency ordering
	if pending[0].Urgency != "critical" {
		t.Errorf("expected critical first, got %s", pending[0].Urgency)
	}

	// Resolve one
	if err := mgr.Resolve(ctx, pending[0].ID, "{}", "done", "human"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	pending, err = mgr.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending after resolve: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("expected 2 pending after resolve, got %d", len(pending))
	}
}

func TestHumanRequestListByAgent(t *testing.T) {
	db := setupHumanRequestDB(t)
	mgr := NewHumanRequestManager(db)
	ctx := context.Background()

	_ = mgr.Create(ctx, HumanRequest{AgentID: "agent-a", Title: "req 1"})
	_ = mgr.Create(ctx, HumanRequest{AgentID: "agent-a", Title: "req 2"})
	_ = mgr.Create(ctx, HumanRequest{AgentID: "agent-b", Title: "req 3"})

	reqs, err := mgr.ListByAgent(ctx, "agent-a")
	if err != nil {
		t.Fatalf("ListByAgent: %v", err)
	}
	if len(reqs) != 2 {
		t.Errorf("expected 2 requests for agent-a, got %d", len(reqs))
	}
}

func TestHumanRequestCreateRequest(t *testing.T) {
	db := setupHumanRequestDB(t)
	mgr := NewHumanRequestManager(db)
	ctx := context.Background()

	id, err := mgr.CreateRequest(ctx, "agent-1", "task-1", "proj-1", "binary", "Install helm", "Need helm v3 for k8s deployments", "high", `{"version":"3.14"}`)
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	r, err := mgr.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if r.RequestType != RequestTypeBinary {
		t.Errorf("expected binary, got %s", r.RequestType)
	}
	if r.Metadata != `{"version":"3.14"}` {
		t.Errorf("expected metadata, got %s", r.Metadata)
	}
}

func TestHumanRequestWaitForResolution(t *testing.T) {
	db := setupHumanRequestDB(t)
	mgr := NewHumanRequestManager(db)
	ctx := context.Background()

	err := mgr.Create(ctx, HumanRequest{ID: "wait-req", Title: "Waiting test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Resolve it in a goroutine after a short delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = mgr.Resolve(context.Background(), "wait-req", `{"done":true}`, "Here you go", "human")
	}()

	r, err := mgr.WaitForResolution(ctx, "wait-req", 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForResolution: %v", err)
	}
	if r.Status != RequestResolved {
		t.Errorf("expected resolved, got %s", r.Status)
	}
}

func TestHumanRequestWaitForResolutionTimeout(t *testing.T) {
	db := setupHumanRequestDB(t)
	mgr := NewHumanRequestManager(db)
	ctx := context.Background()

	err := mgr.Create(ctx, HumanRequest{ID: "timeout-req", Title: "Slow request"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	r, err := mgr.WaitForResolution(ctx, "timeout-req", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForResolution: %v", err)
	}
	if r.Status != RequestExpired {
		t.Errorf("expected expired, got %s", r.Status)
	}
}

func TestHumanRequestCheckExpired(t *testing.T) {
	db := setupHumanRequestDB(t)
	mgr := NewHumanRequestManager(db)
	ctx := context.Background()

	// Insert a request that is already expired (timeout_minutes = 1, created 2 minutes ago)
	_, err := db.Exec(
		`INSERT INTO human_requests (id, title, request_type, urgency, status, timeout_minutes, metadata, created_at) VALUES (?, ?, 'info', 'normal', 'pending', 1, '{}', datetime('now', '-2 minutes'))`,
		"expired-req", "Old request",
	)
	if err != nil {
		t.Fatalf("insert expired request: %v", err)
	}

	if err := mgr.CheckExpired(ctx); err != nil {
		t.Fatalf("CheckExpired: %v", err)
	}

	r, err := mgr.Get(ctx, "expired-req")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if r.Status != RequestExpired {
		t.Errorf("expected expired status, got %s", r.Status)
	}
}
