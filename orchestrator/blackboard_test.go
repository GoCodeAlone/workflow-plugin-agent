package orchestrator

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestBlackboard(t *testing.T) *Blackboard {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	bb := NewBlackboard(db, nil)
	if err := bb.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return bb
}

func TestBlackboardPostAndRead(t *testing.T) {
	bb := newTestBlackboard(t)
	ctx := context.Background()

	art := Artifact{
		Phase:   "design",
		AgentID: "agent-1",
		Type:    "yaml_config",
		Content: map[string]any{"key": "value"},
		Tags:    []string{"tag1"},
	}
	if err := bb.Post(ctx, art); err != nil {
		t.Fatalf("Post: %v", err)
	}

	results, err := bb.Read(ctx, "design", "yaml_config")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(results))
	}
	got := results[0]
	if got.Phase != "design" {
		t.Errorf("phase: want design, got %q", got.Phase)
	}
	if got.Type != "yaml_config" {
		t.Errorf("type: want yaml_config, got %q", got.Type)
	}
	if got.Content["key"] != "value" {
		t.Errorf("content: want value, got %v", got.Content["key"])
	}
	if len(got.Tags) != 1 || got.Tags[0] != "tag1" {
		t.Errorf("tags: got %v", got.Tags)
	}
}

func TestBlackboardReadLatest(t *testing.T) {
	bb := newTestBlackboard(t)
	ctx := context.Background()

	for i, v := range []string{"first", "second", "third"} {
		_ = i
		if err := bb.Post(ctx, Artifact{
			Phase:   "implement",
			AgentID: "agent-1",
			Type:    "config_diff",
			Content: map[string]any{"order": v},
		}); err != nil {
			t.Fatalf("Post: %v", err)
		}
		// Small sleep to ensure ordering by created_at
		time.Sleep(2 * time.Millisecond)
	}

	latest, err := bb.ReadLatest(ctx, "implement")
	if err != nil {
		t.Fatalf("ReadLatest: %v", err)
	}
	if latest == nil {
		t.Fatal("expected artifact, got nil")
	}
	if latest.Content["order"] != "third" {
		t.Errorf("expected latest to be 'third', got %v", latest.Content["order"])
	}
}

func TestBlackboardReadLatestEmpty(t *testing.T) {
	bb := newTestBlackboard(t)
	ctx := context.Background()

	latest, err := bb.ReadLatest(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("ReadLatest: %v", err)
	}
	if latest != nil {
		t.Errorf("expected nil for missing phase, got %+v", latest)
	}
}

func TestBlackboardReadByPhase(t *testing.T) {
	bb := newTestBlackboard(t)
	ctx := context.Background()

	phases := []string{"design", "design", "review"}
	for _, phase := range phases {
		if err := bb.Post(ctx, Artifact{
			Phase:   phase,
			AgentID: "agent-1",
			Type:    "review_findings",
			Content: map[string]any{},
		}); err != nil {
			t.Fatalf("Post: %v", err)
		}
	}

	designArtifacts, err := bb.Read(ctx, "design", "")
	if err != nil {
		t.Fatalf("Read design: %v", err)
	}
	if len(designArtifacts) != 2 {
		t.Errorf("expected 2 design artifacts, got %d", len(designArtifacts))
	}

	reviewArtifacts, err := bb.Read(ctx, "review", "")
	if err != nil {
		t.Fatalf("Read review: %v", err)
	}
	if len(reviewArtifacts) != 1 {
		t.Errorf("expected 1 review artifact, got %d", len(reviewArtifacts))
	}

	all, err := bb.Read(ctx, "", "")
	if err != nil {
		t.Fatalf("Read all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 total artifacts, got %d", len(all))
	}
}

func TestBlackboardSubscribe(t *testing.T) {
	bb := newTestBlackboard(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := bb.Subscribe(ctx, "approve")

	art := Artifact{
		Phase:   "approve",
		AgentID: "agent-2",
		Type:    "approval_decision",
		Content: map[string]any{"approved": true},
	}
	if err := bb.Post(context.Background(), art); err != nil {
		t.Fatalf("Post: %v", err)
	}

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		if got.Phase != "approve" {
			t.Errorf("expected phase approve, got %q", got.Phase)
		}
		if got.Content["approved"] != true {
			t.Errorf("expected approved=true, got %v", got.Content["approved"])
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for artifact on subscriber channel")
	}
}

func TestBlackboardSubscribeAllPhases(t *testing.T) {
	bb := newTestBlackboard(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := bb.Subscribe(ctx, "") // wildcard

	_ = bb.Post(context.Background(), Artifact{
		Phase: "design", AgentID: "a", Type: "yaml_config", Content: map[string]any{},
	})
	_ = bb.Post(context.Background(), Artifact{
		Phase: "review", AgentID: "a", Type: "review_findings", Content: map[string]any{},
	})

	received := 0
	for received < 2 {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Fatal("channel closed unexpectedly")
			}
			received++
		case <-ctx.Done():
			t.Fatalf("timeout: only received %d/2 artifacts", received)
		}
	}
}
