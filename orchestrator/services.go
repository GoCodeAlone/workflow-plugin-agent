package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/executor"
	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/secrets"
)

// This file defines the orchestrator-scoped service interfaces that the (R)
// refactor steps (step_agent_execute, step_approval, step_memory_extract, etc.)
// resolve at Execute time. The concrete structs (Blackboard, MemoryStore,
// ToolRegistry, SecretGuard, ApprovalManager, ...) satisfy these interfaces
// directly — no adapter is needed for the orchestrator-internal consumers
// because the interfaces use orchestrator-scoped types (Artifact, Approval,
// the 6-field MemoryEntry, Webhook, Skill, ...).
//
// resolveServices(app) extracts each interface from app.SvcRegistry(),
// falling back to a Null default when the service is absent. This lets a step
// decide at runtime whether a dependency is:
//   - truly-optional (Null = skip, step still useful), or
//   - required-stateful (Null = typed ErrServiceUnavailable).
//
// The (optional vs required) classification is documented per interface below
// and mirrored from the plan. It is the STEP's responsibility to enforce
// required-stateful by returning ErrServiceUnavailable when resolveServices
// hands back a Null; the Null implementations themselves are always safe
// no-ops (they never panic and never mutate state).

// ErrServiceUnavailable is returned by a step when a required-stateful service
// resolves to its Null default (i.e. it is absent from the registry). Steps
// that classify a service as required-stateful MUST check IsNull(svc) and
// return this error.
var ErrServiceUnavailable = errors.New("orchestrator: required service not available")

// IsNull reports whether the given service value is one of the Null defaults
// (i.e. the service was absent from the registry and resolveServices
// substituted a no-op). Concrete service implementations are never Null.
//
// A step guarding a required-stateful service writes:
//
//	svcs := resolveServices(s.app)
//	if IsNull(svcs.Approval) {
//	    return ErrServiceUnavailable
//	}
func IsNull(svc any) bool {
	_, ok := svc.(nullBearer)
	return ok
}

// nullBearer is implemented by every Null* default. Concrete service structs do
// NOT implement it, so IsNull returns false for them.
type nullBearer interface {
	null()
}

// nullBase is embedded in every Null* default to satisfy nullBearer.
type nullBase struct{}

func (nullBase) null() {}

// ---------------------------------------------------------------------------
// Blackboard
// ---------------------------------------------------------------------------

// BlackboardService is the orchestrator-scoped shared artifact exchange.
//
// Consumers: step_blackboard (BlackboardPostStep/BlackboardReadStep — REQUIRED),
// step_self_improve_diff (postToBlackboard — OPTIONAL: only posts when
// output_to_blackboard=true).
//
// Classification:
//   - step_blackboard: REQUIRED-STATEFUL (errors if absent).
//   - step_self_improve_diff: TRULY-OPTIONAL (skips the post, step still
//     produces the diff).
type BlackboardService interface {
	Post(ctx context.Context, artifact Artifact) error
	Read(ctx context.Context, phase, artifactType string) ([]Artifact, error)
	ReadLatest(ctx context.Context, phase string) (*Artifact, error)
}

// NullBlackboard is a no-op BlackboardService. Post is a no-op; Read/ReadLatest
// return empty/nil. Detect absence via IsNull.
type NullBlackboard struct{ nullBase }

func (NullBlackboard) Post(_ context.Context, _ Artifact) error { return nil }
func (NullBlackboard) Read(_ context.Context, _, _ string) ([]Artifact, error) {
	return nil, nil
}
func (NullBlackboard) ReadLatest(_ context.Context, _ string) (*Artifact, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// ToolRegistry
// ---------------------------------------------------------------------------

// ToolRegistryService is the orchestrator-scoped tool registry.
//
// Consumer: step_agent_execute (TRULY-OPTIONAL — agent loop gracefully
// degrades when no tools are registered; Get/AllDefs/Execute are only called
// when the registry is present and non-empty).
type ToolRegistryService interface {
	Get(name string) (any, bool)
	AllDefs() []provider.ToolDef
	Execute(ctx context.Context, name string, args map[string]any) (any, error)
	Names() []string
	// SetPaginator attaches a ResponsePaginator that truncates large tool outputs.
	// Added in P2-T3: step_agent_execute calls this when the registry is present.
	SetPaginator(rp *ResponsePaginator)
}

// NullToolRegistry is a no-op ToolRegistryService. Get returns (nil,false);
// AllDefs/Names return nil; Execute returns ErrServiceUnavailable.
type NullToolRegistry struct{ nullBase }

func (NullToolRegistry) Get(_ string) (any, bool)         { return nil, false }
func (NullToolRegistry) AllDefs() []provider.ToolDef      { return nil }
func (NullToolRegistry) Names() []string                  { return nil }
func (NullToolRegistry) Execute(_ context.Context, _ string, _ map[string]any) (any, error) {
	return nil, ErrServiceUnavailable
}
func (NullToolRegistry) SetPaginator(_ *ResponsePaginator) {}

// toolRegistryAdapter adapts *ToolRegistry to ToolRegistryService.
// *ToolRegistry.Get returns (plugin.Tool, bool); the interface narrows the
// return to (any, bool) so consumers don't import the plugin package. The
// underlying value is still the concrete plugin.Tool.
type toolRegistryAdapter struct{ tr *ToolRegistry }

func (a toolRegistryAdapter) Get(name string) (any, bool) { return a.tr.Get(name) }
func (a toolRegistryAdapter) AllDefs() []provider.ToolDef { return a.tr.AllDefs() }
func (a toolRegistryAdapter) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	return a.tr.Execute(ctx, name, args)
}
func (a toolRegistryAdapter) Names() []string { return a.tr.Names() }
func (a toolRegistryAdapter) SetPaginator(rp *ResponsePaginator) {
	a.tr.SetPaginator(rp)
}

// ---------------------------------------------------------------------------
// SecretGuard
// ---------------------------------------------------------------------------

// SecretGuardService is the orchestrator-scoped secret redaction / provider.
//
// Consumers:
//   - step_human_request (TRULY-OPTIONAL — autoStoreSecret helper only).
//   - step_webhook (TRULY-OPTIONAL — signature-verification secret lookup).
//   - step_agent_execute (TRULY-OPTIONAL — CheckAndRedact/Redact on messages).
//
// Phase 3 (PR5): the bespoke step_secret_manage/vault_config consumers were
// removed; secret mutations now flow through the engine step.secret_set /
// step.secret_fetch + workflow-plugin-infra secret-admin steps. The Provider /
// LoadSecrets / LoadAllSecrets methods are retained for the lazy-resolve
// redaction path and the optional consumers above.
type SecretGuardService interface {
	LoadSecrets(ctx context.Context, names []string) error
	LoadAllSecrets(ctx context.Context) error
	Redact(text string) string
	CheckAndRedact(msg *provider.Message) bool
	AddKnownSecret(name, value string)
	// Provider returns the underlying secrets.Provider so consumers can
	// Get/Set raw secret values (webhook signature-secret lookup, human-request
	// token auto-store). Added in P2-T3: step_webhook + step_human_request both
	// need direct secret access that the redaction-oriented methods don't cover.
	Provider() secrets.Provider
}

// NullSecretGuard is a no-op SecretGuardService. Redact returns its input
// unchanged; CheckAndRedact returns false (no redaction performed); the load
// methods are no-ops.
type NullSecretGuard struct{ nullBase }

func (NullSecretGuard) LoadSecrets(_ context.Context, _ []string) error { return nil }
func (NullSecretGuard) LoadAllSecrets(_ context.Context) error          { return nil }
func (NullSecretGuard) Redact(text string) string                       { return text }
func (NullSecretGuard) CheckAndRedact(_ *provider.Message) bool         { return false }
func (NullSecretGuard) AddKnownSecret(_, _ string)                      {}
func (NullSecretGuard) Provider() secrets.Provider                      { return nil }

// ---------------------------------------------------------------------------
// ApprovalManager
// ---------------------------------------------------------------------------

// ApprovalService is the orchestrator-scoped approval gate.
//
// Consumers:
//   - step_approval (ApprovalResolveStep — REQUIRED-STATEFUL:
//     Approve/Reject error if absent).
//   - step_agent_execute (TRULY-OPTIONAL — WaitForResolution in
//     handleApprovalWait; agent continues without blocking when absent).
type ApprovalService interface {
	CreateApproval(ctx context.Context, agentID, taskID, action, reason, details string) (string, error)
	Approve(ctx context.Context, id, comment string) error
	Reject(ctx context.Context, id, comment string) error
	Get(ctx context.Context, id string) (*Approval, error)
	WaitForResolution(ctx context.Context, id string, timeout time.Duration) (*Approval, error)
}

// NullApproval is a no-op ApprovalService. Approve/Reject/CreateApproval
// return ErrServiceUnavailable; WaitForResolution returns a Timeout approval so
// the agent loop's optional path degrades to "no approval" rather than blocking.
type NullApproval struct{ nullBase }

func (NullApproval) CreateApproval(_ context.Context, _, _, _, _, _ string) (string, error) {
	return "", ErrServiceUnavailable
}
func (NullApproval) Approve(_ context.Context, _, _ string) error { return ErrServiceUnavailable }
func (NullApproval) Reject(_ context.Context, _, _ string) error  { return ErrServiceUnavailable }
func (NullApproval) Get(_ context.Context, _ string) (*Approval, error) {
	return nil, ErrServiceUnavailable
}
func (NullApproval) WaitForResolution(_ context.Context, _ string, _ time.Duration) (*Approval, error) {
	return &Approval{Status: ApprovalTimeout}, nil
}

// ---------------------------------------------------------------------------
// HumanRequestManager
// ---------------------------------------------------------------------------

// HumanRequestService is the orchestrator-scoped human-request gate.
//
// Consumers:
//   - step_human_request (HumanRequestResolveStep — REQUIRED-STATEFUL:
//     Resolve/Cancel/Get error if absent).
//   - step_agent_execute (TRULY-OPTIONAL — WaitForResolution in
//     handleHumanRequestWait).
type HumanRequestService interface {
	CreateRequest(ctx context.Context, agentID, taskID, projectID, reqType, title, desc, urgency, metadata string) (string, error)
	Resolve(ctx context.Context, id, responseData, comment, resolvedBy string) error
	Cancel(ctx context.Context, id, comment string) error
	Get(ctx context.Context, id string) (*HumanRequest, error)
	WaitForResolution(ctx context.Context, id string, timeout time.Duration) (*HumanRequest, error)
}

// NullHumanRequest is a no-op HumanRequestService. Resolve/Cancel/CreateRequest
// return ErrServiceUnavailable; WaitForResolution returns an Expired request so
// the agent loop's optional path degrades to "request expired" rather than
// blocking.
type NullHumanRequest struct{ nullBase }

func (NullHumanRequest) CreateRequest(_ context.Context, _, _, _, _, _, _, _, _ string) (string, error) {
	return "", ErrServiceUnavailable
}
func (NullHumanRequest) Resolve(_ context.Context, _, _, _, _ string) error { return ErrServiceUnavailable }
func (NullHumanRequest) Cancel(_ context.Context, _, _ string) error        { return ErrServiceUnavailable }
func (NullHumanRequest) Get(_ context.Context, _ string) (*HumanRequest, error) {
	return nil, ErrServiceUnavailable
}
func (NullHumanRequest) WaitForResolution(_ context.Context, _ string, _ time.Duration) (*HumanRequest, error) {
	return &HumanRequest{Status: RequestExpired}, nil
}

// ---------------------------------------------------------------------------
// SubAgentManager
// ---------------------------------------------------------------------------

// SubAgentService is the orchestrator-scoped sub-agent manager.
//
// Consumer: step_agent_execute (TRULY-OPTIONAL — CancelChildren during cleanup;
// the manager is also used by the agent_spawn tool, but that resolves it
// directly, not via this step-level interface).
type SubAgentService interface {
	CountActive(ctx context.Context, parentAgentID string) (int, error)
	Spawn(ctx context.Context, parentAgentID, name, taskDesc, systemPrompt string) (string, error)
	CheckTask(ctx context.Context, taskID string) (status, result string, err error)
	WaitTasks(ctx context.Context, taskIDs []string, timeout time.Duration) (map[string]tools.SubTaskResult, error)
	CancelChildren(ctx context.Context, parentAgentID string) error
}

// NullSubAgent is a no-op SubAgentService. CountActive returns 0; Spawn returns
// ErrServiceUnavailable; CancelChildren is a no-op.
type NullSubAgent struct{ nullBase }

func (NullSubAgent) CountActive(_ context.Context, _ string) (int, error) { return 0, nil }
func (NullSubAgent) Spawn(_ context.Context, _, _, _, _ string) (string, error) {
	return "", ErrServiceUnavailable
}
func (NullSubAgent) CheckTask(_ context.Context, _ string) (string, string, error) {
	return "", "", ErrServiceUnavailable
}
func (NullSubAgent) WaitTasks(_ context.Context, _ []string, _ time.Duration) (map[string]tools.SubTaskResult, error) {
	return nil, ErrServiceUnavailable
}
func (NullSubAgent) CancelChildren(_ context.Context, _ string) error { return nil }

// ---------------------------------------------------------------------------
// SkillManager
// ---------------------------------------------------------------------------

// SkillService is the orchestrator-scoped skill manager.
//
// Consumer: step_agent_execute (TRULY-OPTIONAL — BuildSkillPrompt augments the
// system prompt; agent runs normally without skills).
type SkillService interface {
	GetSkill(ctx context.Context, id string) (*Skill, error)
	ListSkills(ctx context.Context) ([]Skill, error)
	BuildSkillPrompt(ctx context.Context, agentID string) (string, error)
	GetAgentSkills(ctx context.Context, agentID string) ([]Skill, error)
}

// NullSkill is a no-op SkillService. BuildSkillPrompt returns "" (no skill
// augmentation); List/Get return nil.
type NullSkill struct{ nullBase }

func (NullSkill) GetSkill(_ context.Context, _ string) (*Skill, error) { return nil, nil }
func (NullSkill) ListSkills(_ context.Context) ([]Skill, error)       { return nil, nil }
func (NullSkill) BuildSkillPrompt(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (NullSkill) GetAgentSkills(_ context.Context, _ string) ([]Skill, error) { return nil, nil }

// ---------------------------------------------------------------------------
// ContainerManager
// ---------------------------------------------------------------------------

// ContainerService is the orchestrator-scoped Docker sandbox manager.
//
// Consumer: step_container_control (TRULY-OPTIONAL — returns status=
// "unavailable" when absent or Docker is not present).
type ContainerService interface {
	IsAvailable() bool
	EnsureContainer(ctx context.Context, projectID, workspacePath string, spec sandboxSpec) (string, error)
	ExecInContainer(ctx context.Context, projectID, command, workDir string, timeout int) (stdout, stderr string, exitCode int, err error)
	StopContainer(ctx context.Context, projectID string) error
	RemoveContainer(ctx context.Context, projectID string) error
	GetContainerStatus(ctx context.Context, projectID string) (string, error)
}

// sandboxSpec is an alias of executor.SandboxConfig so this file does not need
// to re-declare the struct. The concrete ContainerManager already accepts
// executor.SandboxConfig, which is the same type.
type sandboxSpec = executor.SandboxConfig

// NullContainer is a no-op ContainerService. IsAvailable returns false; all
// lifecycle methods return ErrServiceUnavailable.
type NullContainer struct{ nullBase }

func (NullContainer) IsAvailable() bool { return false }
func (NullContainer) EnsureContainer(_ context.Context, _, _ string, _ sandboxSpec) (string, error) {
	return "", ErrServiceUnavailable
}
func (NullContainer) ExecInContainer(_ context.Context, _, _, _ string, _ int) (string, string, int, error) {
	return "", "", -1, ErrServiceUnavailable
}
func (NullContainer) StopContainer(_ context.Context, _ string) error   { return ErrServiceUnavailable }
func (NullContainer) RemoveContainer(_ context.Context, _ string) error { return ErrServiceUnavailable }
func (NullContainer) GetContainerStatus(_ context.Context, _ string) (string, error) {
	return "unavailable", nil
}

// containerAdapter adapts *ContainerManager to ContainerService.
// *ContainerManager.EnsureContainer takes executor.SandboxConfig; the interface
// exposes the same type via the sandboxSpec alias, so the adapter is a thin
// pass-through.
type containerAdapter struct{ cm *ContainerManager }

func (a containerAdapter) IsAvailable() bool { return a.cm.IsAvailable() }
func (a containerAdapter) EnsureContainer(ctx context.Context, projectID, workspacePath string, spec sandboxSpec) (string, error) {
	return a.cm.EnsureContainer(ctx, projectID, workspacePath, spec)
}
func (a containerAdapter) ExecInContainer(ctx context.Context, projectID, command, workDir string, timeout int) (string, string, int, error) {
	return a.cm.ExecInContainer(ctx, projectID, command, workDir, timeout)
}
func (a containerAdapter) StopContainer(ctx context.Context, projectID string) error {
	return a.cm.StopContainer(ctx, projectID)
}
func (a containerAdapter) RemoveContainer(ctx context.Context, projectID string) error {
	return a.cm.RemoveContainer(ctx, projectID)
}
func (a containerAdapter) GetContainerStatus(ctx context.Context, projectID string) (string, error) {
	return a.cm.GetContainerStatus(ctx, projectID)
}

// ---------------------------------------------------------------------------
// WebhookManager
// ---------------------------------------------------------------------------

// WebhookService is the orchestrator-scoped webhook manager.
//
// Consumer: step_webhook (WebhookProcessStep — REQUIRED-STATEFUL: errors if
// absent).
type WebhookService interface {
	GetBySource(ctx context.Context, source string) ([]Webhook, error)
	ExtractEventType(source string, headers map[string]string, payload map[string]any) string
	VerifySignature(source, secret string, payload []byte, signature, timestamp string) bool
	MatchesFilter(source, eventType, filter string) bool
	RenderTaskTemplate(tmpl string, payload map[string]any) (title, description string, err error)
}

// NullWebhook is a no-op WebhookService. GetBySource returns nil; VerifySignature
// returns false (reject); RenderTaskTemplate returns ErrServiceUnavailable.
type NullWebhook struct{ nullBase }

func (NullWebhook) GetBySource(_ context.Context, _ string) ([]Webhook, error) { return nil, nil }
func (NullWebhook) ExtractEventType(_ string, _ map[string]string, _ map[string]any) string {
	return ""
}
func (NullWebhook) VerifySignature(_, _ string, _ []byte, _, _ string) bool { return false }
func (NullWebhook) MatchesFilter(_, _, _ string) bool                       { return false }
func (NullWebhook) RenderTaskTemplate(_ string, _ map[string]any) (string, string, error) {
	return "", "", ErrServiceUnavailable
}

// ---------------------------------------------------------------------------
// MemoryStore
// ---------------------------------------------------------------------------

// MemoryStoreService is the orchestrator-scoped persistent memory store.
//
// DESIGN DECISION (P6 — MemoryEntry translation): the executor package defines
// its own MemoryStore interface backed by a 4-field MemoryEntry
// (ID/AgentID/Content/Category). The orchestrator's concrete MemoryStore uses
// a 6-field MemoryEntry (+Embedding []float32 +CreatedAt time.Time) and its
// Search/Save/ExtractAndSave methods operate on that richer type.
//
// Rather than write a lossy adapter that drops Embedding/CreatedAt, we define
// the orchestrator-scoped MemoryStoreService using the orchestrator's own
// MemoryEntry. The concrete *MemoryStore satisfies it directly — no adapter,
// no data loss. The executor's narrower MemoryStore remains a separate concern
// consumed by the executor loop (which constructs its own MemoryEntry values).
//
// Consumers:
//   - step_memory_extract (TRULY-OPTIONAL — ExtractAndSave; returns success=
//     false when absent, step still completes).
//   - step_agent_execute (TRULY-OPTIONAL — Search for context injection;
//     ExtractAndSave after the run).
type MemoryStoreService interface {
	Search(ctx context.Context, agentID, query string, limit int) ([]MemoryEntry, error)
	Save(ctx context.Context, entry MemoryEntry) error
	ExtractAndSave(ctx context.Context, agentID, transcript string, embedder provider.Embedder) error
}

// NullMemoryStore is a no-op MemoryStoreService. Search returns nil; Save and
// ExtractAndSave are no-ops.
type NullMemoryStore struct{ nullBase }

func (NullMemoryStore) Search(_ context.Context, _, _ string, _ int) ([]MemoryEntry, error) {
	return nil, nil
}
func (NullMemoryStore) Save(_ context.Context, _ MemoryEntry) error { return nil }
func (NullMemoryStore) ExtractAndSave(_ context.Context, _, _ string, _ provider.Embedder) error {
	return nil
}

// ---------------------------------------------------------------------------
// TranscriptRecorder
// ---------------------------------------------------------------------------

// TranscriptService is the orchestrator-scoped transcript recorder.
//
// Consumer: step_agent_execute (TRULY-OPTIONAL — Record is guarded by nil
// checks throughout the agent loop).
//
// Note: the executor package defines a TranscriptRecorder *interface* (with its
// own TranscriptEntry type). The orchestrator's concrete TranscriptRecorder is
// a *struct* whose Record method takes the orchestrator's TranscriptEntry.
// This interface uses the orchestrator-scoped type so the concrete struct
// satisfies it directly.
type TranscriptService interface {
	Record(ctx context.Context, entry TranscriptEntry) error
}

// NullTranscript is a no-op TranscriptService. Record is a no-op.
type NullTranscript struct{ nullBase }

func (NullTranscript) Record(_ context.Context, _ TranscriptEntry) error { return nil }

// ---------------------------------------------------------------------------
// resolveServices
// ---------------------------------------------------------------------------

// serviceBundle is the set of orchestrator service interfaces resolved from the
// application's service registry. Every service field is non-nil: an absent
// service is represented by its Null default. Steps inspect IsNull(iface) to
// distinguish "present" from "absent". DB is nil when "ratchet-db" is absent.
type serviceBundle struct {
	Blackboard   BlackboardService
	ToolRegistry ToolRegistryService
	SecretGuard  SecretGuardService
	Approval     ApprovalService
	HumanRequest HumanRequestService
	SubAgent     SubAgentService
	Skill        SkillService
	Container    ContainerService
	Webhook      WebhookService
	Memory       MemoryStoreService
	Transcript   TranscriptService
	DB           module.DBProvider // nil when "ratchet-db" is absent (steps that need it MUST nil-check)
}

// resolveServices extracts every orchestrator service interface from the
// application's service registry, falling back to Null defaults for absent
// services. It never panics: an unknown value or a failed type assertion yields
// the Null default for that service.
//
// Concrete structs are looked up by their canonical registry key (the same keys
// plugin.go registers). Where the concrete struct's method signatures don't
// exactly match the interface (ToolRegistry.Get returns plugin.Tool, not any),
// a thin adapter wraps the concrete value.
//
// This is the single entry point the (R) refactor steps will call at the top of
// Execute, replacing the scattered app.SvcRegistry()["..."].(*ConcreteType)
// casts.
func resolveServices(app modular.Application) serviceBundle {
	// app==nil early return: the gRPC legacy-bridge path constructs steps with
	// app=nil (typed_contracts.go AgentPlugin.CreateStep -> factory(name,
	// config, nil)). The real-app path below dereferences app.SvcRegistry()
	// (the next statement), which would panic. Instead return the same
	// all-Null bundle the function builds for an empty registry: every
	// required-stateful caller checks IsNull(svc) and degrades gracefully, and
	// every truly-optional caller already tolerates the Null default. See
	// stateless_nilapp_test.go (transitive-call audit) — step_self_improve_diff
	// reaches this path transitively via postToBlackboard.
	if app == nil {
		return serviceBundle{
			Blackboard:   NullBlackboard{},
			ToolRegistry: NullToolRegistry{},
			SecretGuard:  NullSecretGuard{},
			Approval:     NullApproval{},
			HumanRequest: NullHumanRequest{},
			SubAgent:     NullSubAgent{},
			Skill:        NullSkill{},
			Container:    NullContainer{},
			Webhook:      NullWebhook{},
			Memory:       NullMemoryStore{},
			Transcript:   NullTranscript{},
		}
	}
	reg := app.SvcRegistry()
	b := serviceBundle{
		Blackboard:   NullBlackboard{},
		ToolRegistry: NullToolRegistry{},
		SecretGuard:  NullSecretGuard{},
		Approval:     NullApproval{},
		HumanRequest: NullHumanRequest{},
		SubAgent:     NullSubAgent{},
		Skill:        NullSkill{},
		Container:    NullContainer{},
		Webhook:      NullWebhook{},
		Memory:       NullMemoryStore{},
		Transcript:   NullTranscript{},
	}

	if svc, ok := reg["ratchet-blackboard"].(*Blackboard); ok && svc != nil {
		b.Blackboard = svc
	}
	if svc, ok := reg["ratchet-tool-registry"].(*ToolRegistry); ok && svc != nil {
		b.ToolRegistry = toolRegistryAdapter{tr: svc}
	}
	if svc, ok := reg["ratchet-secret-guard"].(*SecretGuard); ok && svc != nil {
		b.SecretGuard = svc
	}
	if svc, ok := reg["ratchet-approval-manager"].(*ApprovalManager); ok && svc != nil {
		b.Approval = svc
	}
	if svc, ok := reg["ratchet-human-request-manager"].(*HumanRequestManager); ok && svc != nil {
		b.HumanRequest = svc
	}
	if svc, ok := reg["ratchet-sub-agent-manager"].(*SubAgentManager); ok && svc != nil {
		b.SubAgent = svc
	}
	if svc, ok := reg["ratchet-skill-manager"].(*SkillManager); ok && svc != nil {
		b.Skill = svc
	}
	if svc, ok := reg["ratchet-container-manager"].(*ContainerManager); ok && svc != nil {
		b.Container = containerAdapter{cm: svc}
	}
	if svc, ok := reg["ratchet-webhook-manager"].(*WebhookManager); ok && svc != nil {
		b.Webhook = svc
	}
	if svc, ok := reg["ratchet-memory-store"].(*MemoryStore); ok && svc != nil {
		b.Memory = svc
	}
	if svc, ok := reg["ratchet-transcript-recorder"].(*TranscriptRecorder); ok && svc != nil {
		b.Transcript = svc
	}
	if svc, ok := reg["ratchet-db"].(module.DBProvider); ok && svc != nil {
		b.DB = svc
	}

	return b
}

// String renders a human-readable summary of which services are present vs
// Null, for diagnostics and test assertions.
func (b serviceBundle) String() string {
	present := 0
	const total = 11
	checks := []struct {
		name string
		n    any
	}{
		{"blackboard", b.Blackboard},
		{"tool_registry", b.ToolRegistry},
		{"secret_guard", b.SecretGuard},
		{"approval", b.Approval},
		{"human_request", b.HumanRequest},
		{"sub_agent", b.SubAgent},
		{"skill", b.Skill},
		{"container", b.Container},
		{"webhook", b.Webhook},
		{"memory", b.Memory},
		{"transcript", b.Transcript},
	}
	for _, c := range checks {
		if !IsNull(c.n) {
			present++
		}
	}
	db := "absent"
	if b.DB != nil {
		db = "present"
	}
	return fmt.Sprintf("serviceBundle{present=%d/%d (db=%s)}", present, total, db)
}

// Compile-time assertions that the Null defaults satisfy their interfaces,
// that the adapters satisfy theirs, and that the concrete structs that need no
// adapter satisfy their interfaces directly.
var (
	// Null defaults.
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

	// Adapters.
	_ ToolRegistryService = toolRegistryAdapter{}
	_ ContainerService    = containerAdapter{}

	// Concrete structs that satisfy their interface directly (no adapter).
	_ BlackboardService    = (*Blackboard)(nil)
	_ SecretGuardService   = (*SecretGuard)(nil)
	_ ApprovalService      = (*ApprovalManager)(nil)
	_ HumanRequestService  = (*HumanRequestManager)(nil)
	_ SubAgentService      = (*SubAgentManager)(nil)
	_ SkillService         = (*SkillManager)(nil)
	_ WebhookService       = (*WebhookManager)(nil)
	_ MemoryStoreService   = (*MemoryStore)(nil)
	_ TranscriptService    = (*TranscriptRecorder)(nil)
)
