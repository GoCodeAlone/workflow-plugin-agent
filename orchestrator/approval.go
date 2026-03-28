package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ApprovalStatus represents the resolution state of an approval.
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalRejected ApprovalStatus = "rejected"
	ApprovalTimeout  ApprovalStatus = "timeout"
)

// Approval represents a human approval request.
type Approval struct {
	ID              string
	AgentID         string
	TaskID          string
	Action          string
	Reason          string
	Details         string
	Status          ApprovalStatus
	ReviewerComment string
	CreatedAt       time.Time
	ResolvedAt      *time.Time
	TimeoutMinutes  int
}

// ApprovalManager manages approval records in the database and pushes SSE notifications.
type ApprovalManager struct {
	db             *sql.DB
	sseHub         *SSEHub
	defaultTimeout int // minutes
}

// NewApprovalManager creates a new ApprovalManager with default 30-minute timeout.
func NewApprovalManager(db *sql.DB) *ApprovalManager {
	return &ApprovalManager{
		db:             db,
		defaultTimeout: 30,
	}
}

// SetSSEHub wires the SSE hub for push notifications.
func (am *ApprovalManager) SetSSEHub(hub *SSEHub) {
	am.sseHub = hub
}

// Create inserts a new approval record and optionally pushes an SSE event.
func (am *ApprovalManager) Create(ctx context.Context, approval Approval) error {
	if approval.ID == "" {
		approval.ID = uuid.New().String()
	}
	if approval.TimeoutMinutes == 0 {
		approval.TimeoutMinutes = am.defaultTimeout
	}

	_, err := am.db.ExecContext(ctx,
		`INSERT INTO approvals (id, agent_id, task_id, action, reason, details, status, timeout_minutes, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, datetime('now'))`,
		approval.ID, approval.AgentID, approval.TaskID,
		approval.Action, approval.Reason, approval.Details,
		approval.TimeoutMinutes,
	)
	if err != nil {
		return fmt.Errorf("create approval: %w", err)
	}

	am.pushSSEEvent("approval_requested", map[string]any{
		"id":      approval.ID,
		"action":  approval.Action,
		"reason":  approval.Reason,
		"details": approval.Details,
		"status":  "pending",
	})

	return nil
}

// CreateApproval is a convenience method that satisfies the tools.ApprovalCreator interface.
// It creates an Approval via Create() and returns the generated ID.
func (am *ApprovalManager) CreateApproval(ctx context.Context, agentID, taskID, action, reason, details string) (string, error) {
	id := uuid.New().String()
	a := Approval{
		ID:      id,
		AgentID: agentID,
		TaskID:  taskID,
		Action:  action,
		Reason:  reason,
		Details: details,
	}
	if err := am.Create(ctx, a); err != nil {
		return "", err
	}
	return id, nil
}

// Approve marks an approval as approved and records the reviewer's comment.
func (am *ApprovalManager) Approve(ctx context.Context, id string, comment string) error {
	_, err := am.db.ExecContext(ctx,
		`UPDATE approvals SET status = 'approved', reviewer_comment = ?, resolved_at = datetime('now') WHERE id = ? AND status = 'pending'`,
		comment, id,
	)
	if err != nil {
		return fmt.Errorf("approve: %w", err)
	}

	am.pushSSEEvent("approval_resolved", map[string]any{
		"id":               id,
		"status":           "approved",
		"reviewer_comment": comment,
	})

	return nil
}

// Reject marks an approval as rejected and records the reviewer's comment.
func (am *ApprovalManager) Reject(ctx context.Context, id string, comment string) error {
	_, err := am.db.ExecContext(ctx,
		`UPDATE approvals SET status = 'rejected', reviewer_comment = ?, resolved_at = datetime('now') WHERE id = ? AND status = 'pending'`,
		comment, id,
	)
	if err != nil {
		return fmt.Errorf("reject: %w", err)
	}

	am.pushSSEEvent("approval_resolved", map[string]any{
		"id":               id,
		"status":           "rejected",
		"reviewer_comment": comment,
	})

	return nil
}

// Get retrieves an approval by ID.
func (am *ApprovalManager) Get(ctx context.Context, id string) (*Approval, error) {
	row := am.db.QueryRowContext(ctx,
		`SELECT id, agent_id, task_id, action, reason, details, status, reviewer_comment, timeout_minutes, created_at, resolved_at
		 FROM approvals WHERE id = ?`, id,
	)
	return scanApproval(row)
}

// ListPending returns all approvals with status 'pending'.
func (am *ApprovalManager) ListPending(ctx context.Context) ([]Approval, error) {
	rows, err := am.db.QueryContext(ctx,
		`SELECT id, agent_id, task_id, action, reason, details, status, reviewer_comment, timeout_minutes, created_at, resolved_at
		 FROM approvals WHERE status = 'pending' ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var approvals []Approval
	for rows.Next() {
		a, err := scanApprovalRow(rows)
		if err != nil {
			return nil, err
		}
		approvals = append(approvals, *a)
	}
	return approvals, rows.Err()
}

// CheckTimeout marks any pending approvals that have exceeded their timeout.
func (am *ApprovalManager) CheckTimeout(ctx context.Context) error {
	_, err := am.db.ExecContext(ctx,
		`UPDATE approvals
		 SET status = 'timeout', resolved_at = datetime('now')
		 WHERE status = 'pending'
		 AND datetime(created_at, '+' || timeout_minutes || ' minutes') < datetime('now')`,
	)
	if err != nil {
		return fmt.Errorf("check timeout: %w", err)
	}
	return nil
}

// WaitForResolution polls the database every 2 seconds until the approval is resolved or
// the given timeout elapses. Returns the resolved approval or an error.
func (am *ApprovalManager) WaitForResolution(ctx context.Context, id string, timeout time.Duration) (*Approval, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		approval, err := am.Get(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("wait for resolution: %w", err)
		}

		if approval.Status != ApprovalPending {
			return approval, nil
		}

		if time.Now().After(deadline) {
			// Mark as timed out
			_, _ = am.db.ExecContext(ctx,
				`UPDATE approvals SET status = 'timeout', resolved_at = datetime('now') WHERE id = ? AND status = 'pending'`,
				id,
			)
			approval.Status = ApprovalTimeout
			return approval, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			// Poll again
		}
	}
}

// pushSSEEvent sends a JSON-encoded SSE event if the hub is configured.
func (am *ApprovalManager) pushSSEEvent(eventType string, payload map[string]any) {
	if am.sseHub == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	am.sseHub.BroadcastEvent(eventType, string(data))
}

// scanApproval scans a single *sql.Row into an Approval.
func scanApproval(row *sql.Row) (*Approval, error) {
	var a Approval
	var resolvedAt sql.NullTime
	var createdAt string
	err := row.Scan(
		&a.ID, &a.AgentID, &a.TaskID, &a.Action, &a.Reason, &a.Details,
		&a.Status, &a.ReviewerComment, &a.TimeoutMinutes, &createdAt, &resolvedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan approval: %w", err)
	}
	if t, parseErr := time.Parse("2006-01-02 15:04:05", createdAt); parseErr == nil {
		a.CreatedAt = t
	}
	if resolvedAt.Valid {
		a.ResolvedAt = &resolvedAt.Time
	}
	return &a, nil
}

// scanApprovalRow scans a *sql.Rows into an Approval.
func scanApprovalRow(rows *sql.Rows) (*Approval, error) {
	var a Approval
	var resolvedAt sql.NullTime
	var createdAt string
	err := rows.Scan(
		&a.ID, &a.AgentID, &a.TaskID, &a.Action, &a.Reason, &a.Details,
		&a.Status, &a.ReviewerComment, &a.TimeoutMinutes, &createdAt, &resolvedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan approval row: %w", err)
	}
	if t, parseErr := time.Parse("2006-01-02 15:04:05", createdAt); parseErr == nil {
		a.CreatedAt = t
	}
	if resolvedAt.Valid {
		a.ResolvedAt = &resolvedAt.Time
	}
	return &a, nil
}
