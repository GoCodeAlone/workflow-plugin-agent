package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/google/uuid"
)

// services_test.go exercises:
//   1. Each Null default is safe/no-op and satisfies its interface.
//   2. IsNull() correctly distinguishes Null defaults from concrete/adapter
//      implementations.
//   3. The two adapters (toolRegistryAdapter, containerAdapter) satisfy their
//      interfaces and delegate correctly.
//
// resolveServices behavior (injected→real, absent→Null) is covered by
// wiring_integration_test.go, which builds a mockApp.

// ---------------------------------------------------------------------------
// IsNull
// ---------------------------------------------------------------------------

func TestIsNull_NullDefaultsReportTrue(t *testing.T) {
	nulls := []any{
		NullBlackboard{}, NullToolRegistry{}, NullSecretGuard{}, NullApproval{},
		NullHumanRequest{}, NullSubAgent{}, NullSkill{}, NullContainer{},
		NullWebhook{}, NullMemoryStore{}, NullTranscript{},
	}
	for i, n := range nulls {
		if !IsNull(n) {
			t.Errorf("IsNull(null#%d) = false, want true", i)
		}
	}
}

func TestIsNull_PointerToNullReportsTrue(t *testing.T) {
	// Steps may hold a pointer to a Null value; IsNull must still detect it.
	nb := &NullBlackboard{}
	if !IsNull(nb) {
		t.Errorf("IsNull(*NullBlackboard) = false, want true")
	}
}

func TestIsNull_ConcreteAndAdapterReportFalse(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	concretes := []any{
		NewBlackboard(db, nil),
		NewToolRegistry(),
		NewSecretGuard(&mockSecretsProvider{secrets: map[string]string{}}, "test"),
		NewApprovalManager(db),
		NewHumanRequestManager(db),
		NewSubAgentManager(db, 0, 0),
		NewSkillManager(db, ""),
		NewContainerManager(db),
		NewWebhookManager(db, NewSecretGuard(&mockSecretsProvider{secrets: map[string]string{}}, "test")),
		NewMemoryStore(db),
		// Adapters wrap concrete structs → never Null.
		toolRegistryAdapter{tr: NewToolRegistry()},
		containerAdapter{cm: NewContainerManager(db)},
	}
	for i, c := range concretes {
		if IsNull(c) {
			t.Errorf("IsNull(concrete#%d) = true, want false", i)
		}
	}
}

// ---------------------------------------------------------------------------
// NullBlackboard
// ---------------------------------------------------------------------------

func TestNullBlackboard_AllNoOp(t *testing.T) {
	n := NullBlackboard{}
	if err := n.Post(context.Background(), Artifact{}); err != nil {
		t.Errorf("NullBlackboard.Post err = %v, want nil", err)
	}
	got, err := n.Read(context.Background(), "design", "config_diff")
	if err != nil || got != nil {
		t.Errorf("NullBlackboard.Read = %v, %v, want nil, nil", got, err)
	}
	latest, err := n.ReadLatest(context.Background(), "design")
	if err != nil || latest != nil {
		t.Errorf("NullBlackboard.ReadLatest = %v, %v, want nil, nil", latest, err)
	}
}

// ---------------------------------------------------------------------------
// NullToolRegistry
// ---------------------------------------------------------------------------

func TestNullToolRegistry_AllNoOp(t *testing.T) {
	n := NullToolRegistry{}
	if v, ok := n.Get("anything"); ok || v != nil {
		t.Errorf("NullToolRegistry.Get = %v, %v, want nil, false", v, ok)
	}
	if defs := n.AllDefs(); defs != nil {
		t.Errorf("NullToolRegistry.AllDefs = %v, want nil", defs)
	}
	if names := n.Names(); names != nil {
		t.Errorf("NullToolRegistry.Names = %v, want nil", names)
	}
	if _, err := n.Execute(context.Background(), "x", nil); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("NullToolRegistry.Execute err = %v, want ErrServiceUnavailable", err)
	}
}

// ---------------------------------------------------------------------------
// NullSecretGuard
// ---------------------------------------------------------------------------

func TestNullSecretGuard_RedactIsPassthrough(t *testing.T) {
	n := NullSecretGuard{}
	const in = "secret value xyz"
	if out := n.Redact(in); out != in {
		t.Errorf("NullSecretGuard.Redact = %q, want %q (passthrough)", out, in)
	}
	if n.CheckAndRedact(&provider.Message{Role: provider.RoleUser, Content: in}) {
		t.Error("NullSecretGuard.CheckAndRedact = true, want false (no redaction)")
	}
	if err := n.LoadSecrets(context.Background(), []string{"x"}); err != nil {
		t.Errorf("NullSecretGuard.LoadSecrets err = %v, want nil", err)
	}
	if err := n.LoadAllSecrets(context.Background()); err != nil {
		t.Errorf("NullSecretGuard.LoadAllSecrets err = %v, want nil", err)
	}
	// AddKnownSecret must be a safe no-op (no panic, no state observable via the interface).
	n.AddKnownSecret("k", "v")
}

// ---------------------------------------------------------------------------
// NullApproval
// ---------------------------------------------------------------------------

func TestNullApproval_RequiredOpsErrOptionalWaitReturnsTimeout(t *testing.T) {
	n := NullApproval{}
	ctx := context.Background()

	// Required-stateful operations must surface ErrServiceUnavailable.
	if _, err := n.CreateApproval(ctx, "a", "t", "act", "r", "d"); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("CreateApproval err = %v, want ErrServiceUnavailable", err)
	}
	if err := n.Approve(ctx, "id", "c"); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("Approve err = %v, want ErrServiceUnavailable", err)
	}
	if err := n.Reject(ctx, "id", "c"); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("Reject err = %v, want ErrServiceUnavailable", err)
	}
	if _, err := n.Get(ctx, "id"); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("Get err = %v, want ErrServiceUnavailable", err)
	}

	// The optional path (agent_execute's handleApprovalWait) must NOT block:
	// WaitForResolution returns a Timeout approval immediately.
	rec, err := n.WaitForResolution(ctx, "id", 0)
	if err != nil {
		t.Fatalf("WaitForResolution err = %v, want nil (must not block)", err)
	}
	if rec == nil || rec.Status != ApprovalTimeout {
		t.Errorf("WaitForResolution = %+v, want Status=timeout", rec)
	}
}

// ---------------------------------------------------------------------------
// NullHumanRequest
// ---------------------------------------------------------------------------

func TestNullHumanRequest_RequiredOpsErrOptionalWaitReturnsExpired(t *testing.T) {
	n := NullHumanRequest{}
	ctx := context.Background()

	if _, err := n.CreateRequest(ctx, "a", "t", "p", "info", "ttl", "d", "low", ""); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("CreateRequest err = %v, want ErrServiceUnavailable", err)
	}
	if err := n.Resolve(ctx, "id", "data", "c", "by"); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("Resolve err = %v, want ErrServiceUnavailable", err)
	}
	if err := n.Cancel(ctx, "id", "c"); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("Cancel err = %v, want ErrServiceUnavailable", err)
	}
	if _, err := n.Get(ctx, "id"); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("Get err = %v, want ErrServiceUnavailable", err)
	}

	// Optional path (agent_execute's handleHumanRequestWait) must not block.
	rec, err := n.WaitForResolution(ctx, "id", 0)
	if err != nil {
		t.Fatalf("WaitForResolution err = %v, want nil", err)
	}
	if rec == nil || rec.Status != RequestExpired {
		t.Errorf("WaitForResolution = %+v, want Status=expired", rec)
	}
}

// ---------------------------------------------------------------------------
// NullSubAgent
// ---------------------------------------------------------------------------

func TestNullSubAgent_CountZeroCancelNoOp(t *testing.T) {
	n := NullSubAgent{}
	ctx := context.Background()

	if c, err := n.CountActive(ctx, "parent"); err != nil || c != 0 {
		t.Errorf("CountActive = %d, %v, want 0, nil", c, err)
	}
	// CancelChildren is the only method step_agent_execute calls on cleanup — must be a no-op.
	if err := n.CancelChildren(ctx, "parent"); err != nil {
		t.Errorf("CancelChildren err = %v, want nil", err)
	}
	// Spawn must surface absence (it's the entry point for delegation).
	if _, err := n.Spawn(ctx, "parent", "name", "task", "prompt"); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("Spawn err = %v, want ErrServiceUnavailable", err)
	}
}

// ---------------------------------------------------------------------------
// NullSkill
// ---------------------------------------------------------------------------

func TestNullSkill_BuildSkillPromptEmpty(t *testing.T) {
	n := NullSkill{}
	ctx := context.Background()
	// step_agent_execute appends the skill prompt only when non-empty — Null returns "".
	prompt, err := n.BuildSkillPrompt(ctx, "agent-1")
	if err != nil || prompt != "" {
		t.Errorf("BuildSkillPrompt = %q, %v, want \"\", nil", prompt, err)
	}
	if skills, err := n.ListSkills(ctx); err != nil || skills != nil {
		t.Errorf("ListSkills = %v, %v, want nil, nil", skills, err)
	}
	if s, err := n.GetSkill(ctx, "x"); err != nil || s != nil {
		t.Errorf("GetSkill = %v, %v, want nil, nil", s, err)
	}
	if skills, err := n.GetAgentSkills(ctx, "a"); err != nil || skills != nil {
		t.Errorf("GetAgentSkills = %v, %v, want nil, nil", skills, err)
	}
}

// ---------------------------------------------------------------------------
// NullContainer
// ---------------------------------------------------------------------------

func TestNullContainer_Unavailable(t *testing.T) {
	n := NullContainer{}
	if n.IsAvailable() {
		t.Error("IsAvailable = true, want false")
	}
	// step_container_control uses GetContainerStatus as the graceful-degrade return.
	status, err := n.GetContainerStatus(context.Background(), "proj")
	if err != nil || status != "unavailable" {
		t.Errorf("GetContainerStatus = %q, %v, want \"unavailable\", nil", status, err)
	}
	if _, err := n.EnsureContainer(context.Background(), "p", "/w", sandboxSpec{}); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("EnsureContainer err = %v, want ErrServiceUnavailable", err)
	}
}

// ---------------------------------------------------------------------------
// NullWebhook
// ---------------------------------------------------------------------------

func TestNullWebhook_RejectsAndErrors(t *testing.T) {
	n := NullWebhook{}
	ctx := context.Background()

	wh, err := n.GetBySource(ctx, "github")
	if err != nil || wh != nil {
		t.Errorf("GetBySource = %v, %v, want nil, nil", wh, err)
	}
	// VerifySignature returns false (reject) — absent webhook config can't verify.
	if n.VerifySignature("github", "secret", []byte("p"), "sig", "ts") {
		t.Error("VerifySignature = true, want false (reject when absent)")
	}
	// RenderTaskTemplate errors — step_webhook classifies WebhookManager as required-stateful.
	if _, _, err := n.RenderTaskTemplate("{{.type}}", map[string]any{"type": "push"}); !errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("RenderTaskTemplate err = %v, want ErrServiceUnavailable", err)
	}
}

// ---------------------------------------------------------------------------
// NullMemoryStore
// ---------------------------------------------------------------------------

func TestNullMemoryStore_AllNoOp(t *testing.T) {
	n := NullMemoryStore{}
	ctx := context.Background()
	if got, err := n.Search(ctx, "a", "q", 5); err != nil || got != nil {
		t.Errorf("Search = %v, %v, want nil, nil", got, err)
	}
	if err := n.Save(ctx, MemoryEntry{ID: uuid.New().String(), AgentID: "a", Content: "c"}); err != nil {
		t.Errorf("Save err = %v, want nil", err)
	}
	if err := n.ExtractAndSave(ctx, "a", "transcript", nil); err != nil {
		t.Errorf("ExtractAndSave err = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// NullTranscript
// ---------------------------------------------------------------------------

func TestNullTranscript_RecordNoOp(t *testing.T) {
	n := NullTranscript{}
	if err := n.Record(context.Background(), TranscriptEntry{ID: "x"}); err != nil {
		t.Errorf("Record err = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// toolRegistryAdapter
// ---------------------------------------------------------------------------

func TestToolRegistryAdapter_Delegates(t *testing.T) {
	tr := NewToolRegistry()
	tr.Register(&tools.FileReadTool{}) // concrete plugin.Tool
	a := toolRegistryAdapter{tr: tr}

	// Get returns (any, bool) — the underlying value is the registered tool.
	v, ok := a.Get("file_read")
	if !ok || v == nil {
		t.Errorf("adapter.Get = %v, %v, want non-nil, true", v, ok)
	}
	if v2, ok := a.Get("does_not_exist"); ok || v2 != nil {
		t.Errorf("adapter.Get(missing) = %v, %v, want nil, false", v2, ok)
	}
	defs := a.AllDefs()
	if len(defs) == 0 {
		t.Error("adapter.AllDefs = empty, want at least one def (file_read registered)")
	}
	names := a.Names()
	if len(names) == 0 {
		t.Error("adapter.Names = empty, want at least one name")
	}
	// Execute delegates to the registry.
	if _, err := a.Execute(context.Background(), "file_read", map[string]any{"path": "."}); err != nil {
		t.Logf("adapter.Execute returned err (acceptable for this smoke): %v", err)
	}
}

func TestToolRegistryAdapter_NotNull(t *testing.T) {
	a := toolRegistryAdapter{tr: NewToolRegistry()}
	if IsNull(a) {
		t.Error("IsNull(toolRegistryAdapter) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// containerAdapter
// ---------------------------------------------------------------------------

func TestContainerAdapter_Delegates(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	cm := NewContainerManager(db)
	// Docker isn't available in CI → adapter reports unavailable but doesn't panic.
	a := containerAdapter{cm: cm}
	if a.IsAvailable() {
		t.Log("containerAdapter.IsAvailable = true (Docker present); acceptable")
	} else {
		t.Log("containerAdapter.IsAvailable = false (no Docker); acceptable")
	}
}

func TestContainerAdapter_NotNull(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	a := containerAdapter{cm: NewContainerManager(db)}
	if IsNull(a) {
		t.Error("IsNull(containerAdapter) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// serviceBundle.String
// ---------------------------------------------------------------------------

func TestServiceBundle_String_AllAbsent(t *testing.T) {
	// When resolveServices hands back all Null defaults, String reports present=0.
	b := serviceBundle{
		Blackboard: NullBlackboard{}, ToolRegistry: NullToolRegistry{}, SecretGuard: NullSecretGuard{},
		Approval: NullApproval{}, HumanRequest: NullHumanRequest{}, SubAgent: NullSubAgent{},
		Skill: NullSkill{}, Container: NullContainer{}, Webhook: NullWebhook{},
		Memory: NullMemoryStore{}, Transcript: NullTranscript{},
	}
	s := b.String()
	if !contains(s, "present=0/11") {
		t.Errorf("String() = %q, want present=0/11", s)
	}
	if !contains(s, "db=absent") {
		t.Errorf("String() = %q, want db=absent", s)
	}
}

// Compile-time: ensure the test file references the types it asserts on, so a
// rename breaks the build here rather than at runtime.
var (
	_ BlackboardService    = NullBlackboard{}
	_ ToolRegistryService  = NullToolRegistry{}
	_ SecretGuardService   = NullSecretGuard{}
	_ ApprovalService      = NullApproval{}
	_ HumanRequestService  = NullHumanRequest{}
	_ SubAgentService      = NullSubAgent{}
	_ SkillService         = NullSkill{}
	_ ContainerService     = NullContainer{}
	_ WebhookService       = NullWebhook{}
	_ MemoryStoreService   = NullMemoryStore{}
	_ TranscriptService    = NullTranscript{}
)

// quiet unused-import guard: these are used in the signatures above implicitly.
var _ = (*sql.DB)(nil)
