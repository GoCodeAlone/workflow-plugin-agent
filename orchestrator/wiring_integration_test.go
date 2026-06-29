package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
)

// wiring_integration_test.go is the P9 primary gate for the orchestrator
// interface-refactor foundation (Phase 2 Task 1). It mirrors SetupE2EAgent's
// approach: build a mockApp, inject concrete services DIRECTLY into
// app.services (NOT via plugin.go's config-driven wiring hooks, which need a
// real DB/SSE/Migrate stack), then exercise resolveServices + a representative
// step end-to-end.
//
// What this test proves:
//  1. resolveServices(injected) → each interface is the REAL concrete/adapter,
//     not a Null default (IsNull == false).
//  2. resolveServices(empty) → every interface is its Null default, no panic.
//  3. resolveServices is robust to junk values in the registry (a wrong-type
//     value yields Null, not a panic).
//  4. A representative REQUIRED-STATEFUL step (BlackboardPostStep) executes
//     end-to-end when its concrete service is injected, and surfaces a clear
//     error when absent — proving the wiring contract resolveServices encodes
//     matches what the steps actually consume.
//
// This test does NOT run plugin.go hooks (no DB schema migrations, no SSE hub,
// no provider config). Only the minimal in-memory SQLite tables the chosen
// representative step needs are created.

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// injectAll wires every concrete orchestrator service into the mockApp under
// its canonical registry key, mirroring what plugin.go's hooks would register
// in a full deployment. It uses in-memory SQLite so no external deps are
// required.
func injectAll(t *testing.T, app *mockApp) *sql.DB {
	t.Helper()
	db := openTestDB(t)

	sg := NewSecretGuard(&mockSecretsProvider{secrets: map[string]string{}}, "test")
	tr := NewToolRegistry()
	bb := NewBlackboard(db, nil)
	if err := bb.Migrate(context.Background()); err != nil {
		t.Fatalf("blackboard migrate: %v", err)
	}
	ms := NewMemoryStore(db)
	if err := ms.InitTables(); err != nil {
		t.Fatalf("memory init tables: %v", err)
	}
	rec := NewTranscriptRecorder(db, sg)

	app.services["ratchet-blackboard"] = bb
	app.services["ratchet-tool-registry"] = tr
	app.services["ratchet-secret-guard"] = sg
	app.services["ratchet-approval-manager"] = NewApprovalManager(db)
	app.services["ratchet-human-request-manager"] = NewHumanRequestManager(db)
	app.services["ratchet-sub-agent-manager"] = NewSubAgentManager(db, 0, 0)
	app.services["ratchet-skill-manager"] = NewSkillManager(db, "")
	app.services["ratchet-container-manager"] = NewContainerManager(db)
	app.services["ratchet-webhook-manager"] = NewWebhookManager(db, sg)
	app.services["ratchet-memory-store"] = ms
	app.services["ratchet-transcript-recorder"] = rec

	return db
}

// injectDB wires only a minimal DBProvider under "ratchet-db" so resolveServices
// can be observed with a DB present. The returned *sql.DB is the same handle.
type stubDBProvider struct{ db *sql.DB }

func (s stubDBProvider) DB() *sql.DB { return s.db }

// ---------------------------------------------------------------------------
// resolveServices: injected → real
// ---------------------------------------------------------------------------

func TestResolveServices_AllInjectedReturnsRealInterfaces(t *testing.T) {
	app := newMockApp()
	db := injectAll(t, app)
	t.Cleanup(func() { _ = db.Close() })

	b := resolveServices(app)

	// Every service interface must be the real implementation, not Null.
	cases := []struct {
		name string
		svc  any
	}{
		{"Blackboard", b.Blackboard},
		{"ToolRegistry", b.ToolRegistry},
		{"SecretGuard", b.SecretGuard},
		{"Approval", b.Approval},
		{"HumanRequest", b.HumanRequest},
		{"SubAgent", b.SubAgent},
		{"Skill", b.Skill},
		{"Container", b.Container},
		{"Webhook", b.Webhook},
		{"Memory", b.Memory},
		{"Transcript", b.Transcript},
	}
	for _, c := range cases {
		if IsNull(c.svc) {
			t.Errorf("resolveServices: %s is Null after injection, want real", c.name)
		}
	}

	// Concrete types surface through the interface where no adapter is used.
	if _, ok := b.Blackboard.(*Blackboard); !ok {
		t.Errorf("Blackboard = %T, want *Blackboard", b.Blackboard)
	}
	if _, ok := b.Memory.(*MemoryStore); !ok {
		t.Errorf("Memory = %T, want *MemoryStore", b.Memory)
	}
	if _, ok := b.Approval.(*ApprovalManager); !ok {
		t.Errorf("Approval = %T, want *ApprovalManager", b.Approval)
	}
	// Adapted types: the interface value is the adapter struct, not the raw pointer.
	if _, ok := b.ToolRegistry.(toolRegistryAdapter); !ok {
		t.Errorf("ToolRegistry = %T, want toolRegistryAdapter", b.ToolRegistry)
	}
	if _, ok := b.Container.(containerAdapter); !ok {
		t.Errorf("Container = %T, want containerAdapter", b.Container)
	}
}

// ---------------------------------------------------------------------------
// resolveServices: absent → Null, no panic
// ---------------------------------------------------------------------------

func TestResolveServices_EmptyAppReturnsAllNulls(t *testing.T) {
	app := newMockApp() // no services injected

	// Must not panic on an empty registry.
	b := resolveServices(app)

	cases := []struct {
		name string
		svc  any
	}{
		{"Blackboard", b.Blackboard},
		{"ToolRegistry", b.ToolRegistry},
		{"SecretGuard", b.SecretGuard},
		{"Approval", b.Approval},
		{"HumanRequest", b.HumanRequest},
		{"SubAgent", b.SubAgent},
		{"Skill", b.Skill},
		{"Container", b.Container},
		{"Webhook", b.Webhook},
		{"Memory", b.Memory},
		{"Transcript", b.Transcript},
	}
	for _, c := range cases {
		if !IsNull(c.svc) {
			t.Errorf("resolveServices(empty): %s = %T, want Null default", c.name, c.svc)
		}
	}
	if b.DB != nil {
		t.Errorf("resolveServices(empty): DB = %T, want nil", b.DB)
	}

	// String() reports all-absent.
	s := b.String()
	if !contains(s, "present=0/11") || !contains(s, "db=absent") {
		t.Errorf("String() = %q, want present=0/11 and db=absent", s)
	}
}

// ---------------------------------------------------------------------------
// resolveServices: wrong-type value under a known key → Null (no panic)
// ---------------------------------------------------------------------------

func TestResolveServices_WrongTypeYieldsNull(t *testing.T) {
	app := newMockApp()
	// Register a deliberately-wrong value under the blackboard key. A failed
	// type assertion must yield the Null default, not a panic.
	app.services["ratchet-blackboard"] = "not-a-blackboard"
	app.services["ratchet-tool-registry"] = 42

	b := resolveServices(app)

	if !IsNull(b.Blackboard) {
		t.Errorf("Blackboard = %T, want Null (wrong type registered)", b.Blackboard)
	}
	if !IsNull(b.ToolRegistry) {
		t.Errorf("ToolRegistry = %T, want Null (wrong type registered)", b.ToolRegistry)
	}
}

// ---------------------------------------------------------------------------
// resolveServices: nil pointer under a key → Null (no panic)
// ---------------------------------------------------------------------------

func TestResolveServices_NilPointerYieldsNull(t *testing.T) {
	app := newMockApp()
	// A typed nil pointer must not satisfy the concrete-type check (ok==false
	// because of the != nil guard), so the Null default is used.
	app.services["ratchet-blackboard"] = (*Blackboard)(nil)
	app.services["ratchet-memory-store"] = (*MemoryStore)(nil)

	b := resolveServices(app)
	if !IsNull(b.Blackboard) {
		t.Errorf("Blackboard = %T, want Null (nil pointer registered)", b.Blackboard)
	}
	if !IsNull(b.Memory) {
		t.Errorf("Memory = %T, want Null (nil pointer registered)", b.Memory)
	}
}

// ---------------------------------------------------------------------------
// resolveServices: DB present
// ---------------------------------------------------------------------------

func TestResolveServices_DBPresent(t *testing.T) {
	app := newMockApp()
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	app.services["ratchet-db"] = stubDBProvider{db: db}

	b := resolveServices(app)
	if b.DB == nil {
		t.Fatal("DB = nil, want stubDBProvider")
	}
	if b.DB.DB() != db {
		t.Error("DB.DB() does not return the injected handle")
	}
	if !contains(b.String(), "db=present") {
		t.Errorf("String() = %q, want db=present", b.String())
	}
}

// ---------------------------------------------------------------------------
// Representative step end-to-end: BlackboardPostStep (REQUIRED-STATEFUL)
// ---------------------------------------------------------------------------

// TestResolveServices_RepresentativeStepEndToEnd proves the wiring contract
// resolveServices encodes matches what the refactored (R) steps consume.
// BlackboardPostStep resolves its dependency via resolveServices(app).Blackboard
// (the interface); resolveServices returns the same concrete *Blackboard under
// the BlackboardService interface. Both paths must agree. When the service is
// injected the step succeeds; when absent it returns a clear
// ErrServiceUnavailable-wrapped error.
func TestResolveServices_RepresentativeStepEndToEnd(t *testing.T) {
	app := newMockApp()
	db := injectAll(t, app)
	t.Cleanup(func() { _ = db.Close() })

	// 1. resolveServices sees the injected blackboard as real.
	b := resolveServices(app)
	if IsNull(b.Blackboard) {
		t.Fatal("precondition: Blackboard is Null after injection")
	}

	// 2. Construct and execute the representative step (uses resolveServices).
	step := &BlackboardPostStep{
		name:         "bb-post-test",
		phase:        "design",
		artifactType: "config_diff",
		agentID:      "agent-1",
		app:          app,
	}
	pc := &module.PipelineContext{
		Current: map[string]any{
			"content": map[string]any{"changed": true},
		},
	}
	res, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("BlackboardPostStep.Execute err = %v, want nil (service injected)", err)
	}
	if res == nil || res.Output["success"] != true {
		t.Errorf("Execute output = %#v, want success=true", res)
	}

	// 3. Read it back via the interface resolveServices returned — proving the
	//    interface and the concrete type see the same persisted state.
	arts, err := b.Blackboard.Read(context.Background(), "design", "config_diff")
	if err != nil {
		t.Fatalf("BlackboardService.Read err = %v", err)
	}
	if len(arts) != 1 {
		t.Errorf("Read returned %d artifacts, want 1", len(arts))
	}
	if len(arts) > 0 {
		if arts[0].Content["changed"] != true {
			t.Errorf("artifact content = %#v, want changed=true", arts[0].Content)
		}
	}
}

// TestResolveServices_RepresentativeStepAbsentSurfacesError proves that when
// the required-stateful service is absent, the step (now refactored to
// resolveServices + IsNull) returns a clear ErrServiceUnavailable-wrapped
// error — and resolveServices simultaneously reports the service as Null. This
// is the contract Tasks 2-3 rely on: a refactored step checks IsNull(iface) and
// returns ErrServiceUnavailable, which callers can match via errors.Is.
func TestResolveServices_RepresentativeStepAbsentSurfacesError(t *testing.T) {
	app := newMockApp() // nothing injected

	b := resolveServices(app)
	if !IsNull(b.Blackboard) {
		t.Fatal("precondition: Blackboard must be Null on empty app")
	}

	step := &BlackboardPostStep{
		name: "bb-post-absent",
		app:  app,
	}
	_, err := step.Execute(context.Background(), &module.PipelineContext{Current: map[string]any{}})
	if err == nil {
		t.Fatal("BlackboardPostStep.Execute err = nil, want error (service absent)")
	}
	// Post-refactor: the step wraps ErrServiceUnavailable so callers can match
	// it via errors.Is rather than substring-matching a bespoke message.
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("BlackboardPostStep.Execute err = %v, want errors.Is ErrServiceUnavailable", err)
	}
}

// ---------------------------------------------------------------------------
// Refactored single-dep (R) steps end-to-end via resolveServices (P2-T2)
// ---------------------------------------------------------------------------

// TestResolveServices_RefactoredBlackboardRoundTrip proves the P2-T2 refactor
// works through the real in-process path: BlackboardPostStep writes via
// resolveServices(app).Blackboard (the BlackboardService interface) and
// BlackboardReadStep reads it back through the SAME interface — no concrete
// *Blackboard cast in either step. This is the pattern proof that Tasks 2-3
// generalize: a refactored step's interface call is behaviorally identical to
// the concrete call it replaced.
func TestResolveServices_RefactoredBlackboardRoundTrip(t *testing.T) {
	app := newMockApp()
	db := injectAll(t, app)
	t.Cleanup(func() { _ = db.Close() })

	// resolveServices sees the injected blackboard as the real *Blackboard.
	b := resolveServices(app)
	if IsNull(b.Blackboard) {
		t.Fatal("precondition: Blackboard is Null after injection")
	}

	// 1. Post via the refactored step (resolveServices path).
	postStep := &BlackboardPostStep{
		name:         "bb-post-iface",
		phase:        "test",
		artifactType: "roundtrip",
		agentID:      "agent-rt",
		app:          app,
	}
	pc := &module.PipelineContext{
		Current: map[string]any{"content": map[string]any{"payload": "hello"}},
	}
	res, err := postStep.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("BlackboardPostStep.Execute err = %v, want nil (service injected)", err)
	}
	if res.Output["success"] != true {
		t.Fatalf("post output = %#v, want success=true", res.Output)
	}

	// 2. Read it back via the refactored read step (same resolveServices path).
	readStep := &BlackboardReadStep{
		name:         "bb-read-iface",
		phase:        "test",
		artifactType: "roundtrip",
		app:          app,
	}
	readRes, err := readStep.Execute(context.Background(), &module.PipelineContext{Current: map[string]any{}})
	if err != nil {
		t.Fatalf("BlackboardReadStep.Execute err = %v, want nil", err)
	}
	if count, _ := readRes.Output["count"].(int); count != 1 {
		t.Fatalf("read count = %d, want 1", count)
	}
	arts, _ := readRes.Output["artifacts"].([]map[string]any)
	if len(arts) != 1 || arts[0]["content"] == nil {
		t.Fatalf("read artifacts = %#v, want 1 with content", arts)
	}

	// 3. The interface-resolved Blackboard and the step see the same state.
	direct, err := b.Blackboard.Read(context.Background(), "test", "roundtrip")
	if err != nil {
		t.Fatalf("BlackboardService.Read err = %v", err)
	}
	if len(direct) != 1 || direct[0].Content["payload"] != "hello" {
		t.Errorf("interface Read = %+v, want payload=hello", direct)
	}
}

// TestResolveServices_RefactoredApprovalRequired proves a SECOND refactored
// single-dep (R) step — ApprovalResolveStep (REQUIRED-STATEFUL on
// ApprovalService) — surfaces ErrServiceUnavailable when absent and resolves
// the real ApprovalManager when injected. This guards that the required-stateful
// classification is applied consistently, not just to the blackboard step.
func TestResolveServices_RefactoredApprovalRequired(t *testing.T) {
	// Absent → typed error.
	app := newMockApp()
	if !IsNull(resolveServices(app).Approval) {
		t.Fatal("precondition: Approval must be Null on empty app")
	}
	absentStep := &ApprovalResolveStep{name: "approval-absent", action: "approve", app: app}
	_, err := absentStep.Execute(context.Background(), &module.PipelineContext{
		Current: map[string]any{"id": "ap-1"},
	})
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("absent ApprovalResolveStep err = %v, want errors.Is ErrServiceUnavailable", err)
	}

	// Injected → resolves real ApprovalManager via the interface.
	app2 := newMockApp()
	db := injectAll(t, app2)
	t.Cleanup(func() { _ = db.Close() })
	// injectAll wires ApprovalManager but does not run a full schema migration;
	// create the approvals table explicitly (mirrors setupApprovalDB in
	// approval_test.go; the DDL constant lives in production db.go).
	if _, err := db.Exec(createApprovalsTable); err != nil {
		t.Fatalf("create approvals table: %v", err)
	}
	if IsNull(resolveServices(app2).Approval) {
		t.Fatal("precondition: Approval must be real after injection")
	}

	am := resolveServices(app2).Approval
	approvalID, err := am.CreateApproval(context.Background(), "agent-x", "task-1", "deploy", "risk", "details")
	if err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}

	resolveStep := &ApprovalResolveStep{name: "approval-resolve", action: "approve", app: app2}
	res, err := resolveStep.Execute(context.Background(), &module.PipelineContext{
		Current: map[string]any{"id": approvalID},
	})
	if err != nil {
		t.Fatalf("ApprovalResolveStep.Execute err = %v, want nil (service injected)", err)
	}
	if res.Output["success"] != true {
		t.Errorf("resolve output = %#v, want success=true", res.Output)
	}
	got, err := am.Get(context.Background(), approvalID)
	if err != nil {
		t.Fatalf("ApprovalService.Get: %v", err)
	}
	if got.Status != ApprovalApproved {
		t.Errorf("approval status = %s, want %s", got.Status, ApprovalApproved)
	}
}

// ---------------------------------------------------------------------------
// Modular.Application compatibility
// ---------------------------------------------------------------------------

// TestResolveServices_AcceptsModularApplication proves resolveServices takes a
// modular.Application (not just *mockApp), so it works against the real
// application type plugin.go builds. *mockApp satisfies modular.Application by
// embedding it.
func TestResolveServices_AcceptsModularApplication(t *testing.T) {
	var app modular.Application = newMockApp()
	b := resolveServices(app) // must compile + not panic
	if !IsNull(b.Blackboard) {
		t.Error("expected Null Blackboard on empty modular.Application")
	}
}
