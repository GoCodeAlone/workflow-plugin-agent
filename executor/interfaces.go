// Package executor provides the core autonomous agent execution loop.
package executor

import (
	"context"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// TranscriptEntry is a single recorded message in the agent conversation.
type TranscriptEntry struct {
	ID         string
	AgentID    string
	TaskID     string
	ProjectID  string
	Iteration  int
	Role       provider.Role
	Content    string
	Thinking   string
	ToolCalls  []provider.ToolCall
	ToolCallID string
}

// TranscriptRecorder records agent interactions.
type TranscriptRecorder interface {
	Record(ctx context.Context, entry TranscriptEntry) error
}

// ApprovalStatus represents the resolution state of an approval.
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalRejected ApprovalStatus = "rejected"
	ApprovalTimeout  ApprovalStatus = "timeout"
)

// ApprovalRecord is a resolved approval request.
type ApprovalRecord struct {
	ID              string
	Status          ApprovalStatus
	ReviewerComment string
}

// Approver manages approval gate blocking.
type Approver interface {
	// WaitForResolution blocks until the approval with the given ID is resolved
	// or the timeout elapses.
	WaitForResolution(ctx context.Context, approvalID string, timeout time.Duration) (*ApprovalRecord, error)
}

// RequestStatus represents the resolution state of a human request.
type RequestStatus string

const (
	RequestPending   RequestStatus = "pending"
	RequestResolved  RequestStatus = "resolved"
	RequestCancelled RequestStatus = "cancelled"
	RequestExpired   RequestStatus = "expired"
)

// RequestType categorizes what the agent needs from the human.
type RequestType string

const (
	RequestTypeToken  RequestType = "token"
	RequestTypeBinary RequestType = "binary"
	RequestTypeAccess RequestType = "access"
	RequestTypeInfo   RequestType = "info"
	RequestTypeCustom RequestType = "custom"
)

// HumanRequest is a resolved human request record.
type HumanRequest struct {
	ID              string
	RequestType     RequestType
	Status          RequestStatus
	ResponseData    string
	ResponseComment string
	Metadata        string // JSON
}

// HumanRequester manages blocking human-request gates.
type HumanRequester interface {
	// WaitForResolution blocks until the request with the given ID is resolved
	// or the timeout elapses.
	WaitForResolution(ctx context.Context, requestID string, timeout time.Duration) (*HumanRequest, error)
}

// MemoryEntry is a single piece of persistent agent memory.
type MemoryEntry struct {
	ID       string
	AgentID  string
	Content  string
	Category string
}

// MemoryStore provides persistent memory storage and retrieval.
type MemoryStore interface {
	// Search finds relevant memories for agentID using query text.
	Search(ctx context.Context, agentID, query string, limit int) ([]MemoryEntry, error)
	// Save persists a memory entry.
	Save(ctx context.Context, entry MemoryEntry) error
	// ExtractAndSave extracts facts from a transcript and saves them.
	ExtractAndSave(ctx context.Context, agentID, transcript string, embedder provider.Embedder) error
}

// SecretRedactor redacts secrets from text.
type SecretRedactor interface {
	// Redact returns the text with known secrets replaced.
	Redact(text string) string
	// CheckAndRedact redacts secrets from a message in place.
	CheckAndRedact(msg *provider.Message)
}

// NullTranscript is a no-op TranscriptRecorder.
type NullTranscript struct{}

func (n *NullTranscript) Record(_ context.Context, _ TranscriptEntry) error { return nil }

// NullApprover is a no-op Approver — always returns "approved".
type NullApprover struct{}

func (n *NullApprover) WaitForResolution(_ context.Context, _ string, _ time.Duration) (*ApprovalRecord, error) {
	return &ApprovalRecord{Status: ApprovalApproved}, nil
}

// NullHumanRequester is a no-op HumanRequester — always returns expired.
type NullHumanRequester struct{}

func (n *NullHumanRequester) WaitForResolution(_ context.Context, _ string, _ time.Duration) (*HumanRequest, error) {
	return &HumanRequest{Status: RequestExpired}, nil
}

// NullMemoryStore is a no-op MemoryStore.
type NullMemoryStore struct{}

func (n *NullMemoryStore) Search(_ context.Context, _, _ string, _ int) ([]MemoryEntry, error) {
	return nil, nil
}

func (n *NullMemoryStore) Save(_ context.Context, _ MemoryEntry) error { return nil }

func (n *NullMemoryStore) ExtractAndSave(_ context.Context, _ string, _ string, _ provider.Embedder) error {
	return nil
}

// NullSecretRedactor is a no-op SecretRedactor.
type NullSecretRedactor struct{}

func (n *NullSecretRedactor) Redact(text string) string { return text }

func (n *NullSecretRedactor) CheckAndRedact(_ *provider.Message) {}

// Action mirrors policy.Action to avoid circular imports.
type Action string

const (
	ActionAllow Action = "allow"
	ActionDeny  Action = "deny"
	ActionAsk   Action = "ask"
)

// TrustEvaluator checks whether a tool call is permitted.
type TrustEvaluator interface {
	// Evaluate returns the trust action for a tool call.
	Evaluate(ctx context.Context, toolName string, args map[string]any) Action
	// EvaluateCommand returns the trust action for a bash command.
	EvaluateCommand(cmd string) Action
	// EvaluatePath returns the trust action for a file path.
	EvaluatePath(path string) Action
}

// ContainerExecutor can run commands inside a Docker container.
type ContainerExecutor interface {
	IsAvailable() bool
	EnsureContainer(ctx context.Context, projectID, workspacePath string, spec SandboxConfig) (string, error)
	ExecInContainer(ctx context.Context, projectID, command, workDir string, timeout int) (stdout, stderr string, exitCode int, err error)
}

// SandboxConfig holds per-agent Docker sandbox settings.
type SandboxConfig struct {
	Enabled      bool          `json:"enabled" yaml:"enabled"`
	Image        string        `json:"image" yaml:"image"`
	Network      string        `json:"network" yaml:"network"`
	Memory       string        `json:"memory" yaml:"memory"`
	CPU          float64       `json:"cpu" yaml:"cpu"`
	Mounts       []SandboxMount `json:"mounts" yaml:"mounts"`
	InitCommands []string      `json:"init" yaml:"init"`
}

// SandboxMount is a bind mount for a sandbox container.
type SandboxMount struct {
	Src      string `json:"src" yaml:"src"`
	Dst      string `json:"dst" yaml:"dst"`
	ReadOnly bool   `json:"readonly" yaml:"readonly"`
}

// NullTrustEvaluator allows everything (no trust enforcement).
type NullTrustEvaluator struct{}

func (n *NullTrustEvaluator) Evaluate(_ context.Context, _ string, _ map[string]any) Action {
	return ActionAllow
}
func (n *NullTrustEvaluator) EvaluateCommand(_ string) Action { return ActionAllow }
func (n *NullTrustEvaluator) EvaluatePath(_ string) Action    { return ActionAllow }
