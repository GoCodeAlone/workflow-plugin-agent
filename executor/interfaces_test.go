package executor

import (
	"context"
	"testing"
	"time"
)

// TestNullApprover_AlwaysApproved verifies NullApprover returns ApprovalApproved.
func TestNullApprover_AlwaysApproved(t *testing.T) {
	a := &NullApprover{}
	rec, err := a.WaitForResolution(context.Background(), "approval-1", 5*time.Second)
	if err != nil {
		t.Fatalf("NullApprover.WaitForResolution: %v", err)
	}
	if rec.Status != ApprovalApproved {
		t.Errorf("Status: want %q, got %q", ApprovalApproved, rec.Status)
	}
}

// TestNullHumanRequester_AlwaysExpired verifies NullHumanRequester returns RequestExpired.
func TestNullHumanRequester_AlwaysExpired(t *testing.T) {
	hr := &NullHumanRequester{}
	req, err := hr.WaitForResolution(context.Background(), "req-1", 5*time.Second)
	if err != nil {
		t.Fatalf("NullHumanRequester.WaitForResolution: %v", err)
	}
	if req.Status != RequestExpired {
		t.Errorf("Status: want %q, got %q", RequestExpired, req.Status)
	}
}

// TestNullTranscript_RecordReturnsNil verifies NullTranscript does not error.
func TestNullTranscript_RecordReturnsNil(t *testing.T) {
	tr := &NullTranscript{}
	err := tr.Record(context.Background(), TranscriptEntry{ID: "e1", AgentID: "agent-1"})
	if err != nil {
		t.Errorf("NullTranscript.Record: want nil, got %v", err)
	}
}

// TestNullMemoryStore_SearchReturnsEmpty verifies NullMemoryStore returns empty slice.
func TestNullMemoryStore_SearchReturnsEmpty(t *testing.T) {
	ms := &NullMemoryStore{}
	entries, err := ms.Search(context.Background(), "agent-1", "query", 5)
	if err != nil {
		t.Fatalf("NullMemoryStore.Search: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(entries))
	}
}

// TestNullMemoryStore_SaveReturnsNil verifies NullMemoryStore.Save is a no-op.
func TestNullMemoryStore_SaveReturnsNil(t *testing.T) {
	ms := &NullMemoryStore{}
	err := ms.Save(context.Background(), MemoryEntry{ID: "m1", AgentID: "a", Content: "fact"})
	if err != nil {
		t.Errorf("NullMemoryStore.Save: want nil, got %v", err)
	}
}

// TestNullSecretRedactor_PassThrough verifies NullSecretRedactor leaves text unchanged.
func TestNullSecretRedactor_PassThrough(t *testing.T) {
	r := &NullSecretRedactor{}
	input := "my secret token abc123"
	got := r.Redact(input)
	if got != input {
		t.Errorf("Redact: want %q, got %q", input, got)
	}
}

// TestApprovalStatusValues verifies all ApprovalStatus constants are non-empty and distinct.
func TestApprovalStatusValues(t *testing.T) {
	statuses := []ApprovalStatus{ApprovalPending, ApprovalApproved, ApprovalRejected, ApprovalTimeout}
	seen := make(map[ApprovalStatus]bool)
	for _, s := range statuses {
		if s == "" {
			t.Error("ApprovalStatus constant is empty")
		}
		if seen[s] {
			t.Errorf("duplicate ApprovalStatus: %q", s)
		}
		seen[s] = true
	}
}

// TestRequestStatusValues verifies all RequestStatus constants are non-empty and distinct.
func TestRequestStatusValues(t *testing.T) {
	statuses := []RequestStatus{RequestPending, RequestResolved, RequestCancelled, RequestExpired}
	seen := make(map[RequestStatus]bool)
	for _, s := range statuses {
		if s == "" {
			t.Error("RequestStatus constant is empty")
		}
		if seen[s] {
			t.Errorf("duplicate RequestStatus: %q", s)
		}
		seen[s] = true
	}
}
