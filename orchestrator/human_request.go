package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

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
	RequestTypeToken  RequestType = "token"  // API keys, PATs, secrets
	RequestTypeBinary RequestType = "binary" // Install a CLI tool or binary
	RequestTypeAccess RequestType = "access" // Grant access to a service/repo
	RequestTypeInfo   RequestType = "info"   // Clarification or instructions
	RequestTypeCustom RequestType = "custom" // Anything else
)

// HumanRequest represents an agent's request for human assistance.
type HumanRequest struct {
	ID              string
	AgentID         string
	TaskID          string
	ProjectID       string
	RequestType     RequestType
	Title           string
	Description     string
	Urgency         string // "low", "normal", "high", "critical"
	Status          RequestStatus
	ResponseData    string // JSON: the human's answer
	ResponseComment string
	ResolvedBy      string
	TimeoutMinutes  int
	Metadata        string // JSON: extra context hints
	CreatedAt       time.Time
	ResolvedAt      *time.Time
}

// HumanRequestManager manages human request records in the database and pushes SSE notifications.
type HumanRequestManager struct {
	db     *sql.DB
	sseHub *SSEHub
}

// NewHumanRequestManager creates a new HumanRequestManager.
func NewHumanRequestManager(db *sql.DB) *HumanRequestManager {
	return &HumanRequestManager{db: db}
}

// SetSSEHub wires the SSE hub for push notifications.
func (m *HumanRequestManager) SetSSEHub(hub *SSEHub) {
	m.sseHub = hub
}

// Create inserts a new human request record and pushes an SSE event.
func (m *HumanRequestManager) Create(ctx context.Context, req HumanRequest) error {
	if req.ID == "" {
		req.ID = uuid.New().String()
	}
	if req.Urgency == "" {
		req.Urgency = "normal"
	}
	if req.RequestType == "" {
		req.RequestType = RequestTypeInfo
	}
	if req.Metadata == "" {
		req.Metadata = "{}"
	}

	_, err := m.db.ExecContext(ctx,
		`INSERT INTO human_requests (id, agent_id, task_id, project_id, request_type, title, description, urgency, status, timeout_minutes, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, datetime('now'))`,
		req.ID, req.AgentID, req.TaskID, req.ProjectID,
		string(req.RequestType), req.Title, req.Description, req.Urgency,
		req.TimeoutMinutes, req.Metadata,
	)
	if err != nil {
		return fmt.Errorf("create human request: %w", err)
	}

	m.pushSSEEvent("human_request_created", map[string]any{
		"id":           req.ID,
		"agent_id":     req.AgentID,
		"task_id":      req.TaskID,
		"request_type": string(req.RequestType),
		"title":        req.Title,
		"urgency":      req.Urgency,
		"status":       "pending",
	})

	return nil
}

// CreateRequest is a convenience method that satisfies the tools.HumanRequestCreator interface.
func (m *HumanRequestManager) CreateRequest(ctx context.Context, agentID, taskID, projectID, reqType, title, desc, urgency, metadata string) (string, error) {
	id := uuid.New().String()
	req := HumanRequest{
		ID:          id,
		AgentID:     agentID,
		TaskID:      taskID,
		ProjectID:   projectID,
		RequestType: RequestType(reqType),
		Title:       title,
		Description: desc,
		Urgency:     urgency,
		Metadata:    metadata,
	}
	if err := m.Create(ctx, req); err != nil {
		return "", err
	}
	return id, nil
}

// Resolve marks a human request as resolved with the provided response data.
func (m *HumanRequestManager) Resolve(ctx context.Context, id, responseData, comment, resolvedBy string) error {
	result, err := m.db.ExecContext(ctx,
		`UPDATE human_requests SET status = 'resolved', response_data = ?, response_comment = ?, resolved_by = ?, resolved_at = datetime('now') WHERE id = ? AND status = 'pending'`,
		responseData, comment, resolvedBy, id,
	)
	if err != nil {
		return fmt.Errorf("resolve human request: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("resolve human request: request %q not found or not pending", id)
	}

	m.pushSSEEvent("human_request_resolved", map[string]any{
		"id":          id,
		"status":      "resolved",
		"resolved_by": resolvedBy,
	})

	return nil
}

// Cancel marks a human request as cancelled.
func (m *HumanRequestManager) Cancel(ctx context.Context, id, comment string) error {
	result, err := m.db.ExecContext(ctx,
		`UPDATE human_requests SET status = 'cancelled', response_comment = ?, resolved_at = datetime('now') WHERE id = ? AND status = 'pending'`,
		comment, id,
	)
	if err != nil {
		return fmt.Errorf("cancel human request: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("cancel human request: request %q not found or not pending", id)
	}

	m.pushSSEEvent("human_request_cancelled", map[string]any{
		"id":     id,
		"status": "cancelled",
	})

	return nil
}

// GetRequest retrieves a human request by ID and returns it as a map[string]any.
// This satisfies the tools.HumanRequestChecker interface.
func (m *HumanRequestManager) GetRequest(ctx context.Context, id string) (map[string]any, error) {
	req, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"id":               req.ID,
		"agent_id":         req.AgentID,
		"task_id":          req.TaskID,
		"project_id":       req.ProjectID,
		"request_type":     string(req.RequestType),
		"title":            req.Title,
		"description":      req.Description,
		"urgency":          req.Urgency,
		"status":           string(req.Status),
		"response_data":    req.ResponseData,
		"response_comment": req.ResponseComment,
		"resolved_by":      req.ResolvedBy,
		"timeout_minutes":  req.TimeoutMinutes,
		"metadata":         req.Metadata,
		"created_at":       req.CreatedAt.Format(time.RFC3339),
	}
	if req.ResolvedAt != nil {
		result["resolved_at"] = req.ResolvedAt.Format(time.RFC3339)
	} else {
		result["resolved_at"] = nil
	}
	return result, nil
}

// Get retrieves a human request by ID.
func (m *HumanRequestManager) Get(ctx context.Context, id string) (*HumanRequest, error) {
	row := m.db.QueryRowContext(ctx,
		`SELECT id, agent_id, task_id, project_id, request_type, title, description, urgency, status, response_data, response_comment, resolved_by, timeout_minutes, metadata, created_at, resolved_at
		 FROM human_requests WHERE id = ?`, id,
	)
	return scanHumanRequest(row)
}

// ListPending returns all human requests with status 'pending', ordered by urgency then creation time.
func (m *HumanRequestManager) ListPending(ctx context.Context) ([]HumanRequest, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, agent_id, task_id, project_id, request_type, title, description, urgency, status, response_data, response_comment, resolved_by, timeout_minutes, metadata, created_at, resolved_at
		 FROM human_requests WHERE status = 'pending'
		 ORDER BY CASE urgency WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 WHEN 'low' THEN 3 END, created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending human requests: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var requests []HumanRequest
	for rows.Next() {
		r, err := scanHumanRequestRow(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, *r)
	}
	return requests, rows.Err()
}

// ListByAgent returns all human requests for a given agent.
func (m *HumanRequestManager) ListByAgent(ctx context.Context, agentID string) ([]HumanRequest, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, agent_id, task_id, project_id, request_type, title, description, urgency, status, response_data, response_comment, resolved_by, timeout_minutes, metadata, created_at, resolved_at
		 FROM human_requests WHERE agent_id = ? ORDER BY created_at DESC`,
		agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list human requests by agent: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var requests []HumanRequest
	for rows.Next() {
		r, err := scanHumanRequestRow(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, *r)
	}
	return requests, rows.Err()
}

// CheckExpired marks any pending requests that have exceeded their timeout (timeout_minutes > 0).
func (m *HumanRequestManager) CheckExpired(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx,
		`UPDATE human_requests
		 SET status = 'expired', resolved_at = datetime('now')
		 WHERE status = 'pending'
		 AND timeout_minutes > 0
		 AND datetime(created_at, '+' || timeout_minutes || ' minutes') < datetime('now')`,
	)
	if err != nil {
		return fmt.Errorf("check expired: %w", err)
	}
	return nil
}

// WaitForResolution polls the database every 2 seconds until the request is resolved or
// the given timeout elapses. Returns the resolved request or marks it expired.
func (m *HumanRequestManager) WaitForResolution(ctx context.Context, id string, timeout time.Duration) (*HumanRequest, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		req, err := m.Get(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("wait for resolution: %w", err)
		}

		if req.Status != RequestPending {
			return req, nil
		}

		if time.Now().After(deadline) {
			_, _ = m.db.ExecContext(ctx,
				`UPDATE human_requests SET status = 'expired', resolved_at = datetime('now') WHERE id = ? AND status = 'pending'`,
				id,
			)
			req.Status = RequestExpired
			return req, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// pushSSEEvent sends a JSON-encoded SSE event if the hub is configured.
func (m *HumanRequestManager) pushSSEEvent(eventType string, payload map[string]any) {
	if m.sseHub == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	m.sseHub.BroadcastEvent(eventType, string(data))
}

// scanHumanRequest scans a single *sql.Row into a HumanRequest.
func scanHumanRequest(row *sql.Row) (*HumanRequest, error) {
	var r HumanRequest
	var resolvedAt sql.NullTime
	var createdAt string
	err := row.Scan(
		&r.ID, &r.AgentID, &r.TaskID, &r.ProjectID,
		&r.RequestType, &r.Title, &r.Description, &r.Urgency,
		&r.Status, &r.ResponseData, &r.ResponseComment, &r.ResolvedBy,
		&r.TimeoutMinutes, &r.Metadata, &createdAt, &resolvedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan human request: %w", err)
	}
	if t, parseErr := time.Parse("2006-01-02 15:04:05", createdAt); parseErr == nil {
		r.CreatedAt = t
	}
	if resolvedAt.Valid {
		r.ResolvedAt = &resolvedAt.Time
	}
	return &r, nil
}

// scanHumanRequestRow scans a *sql.Rows into a HumanRequest.
func scanHumanRequestRow(rows *sql.Rows) (*HumanRequest, error) {
	var r HumanRequest
	var resolvedAt sql.NullTime
	var createdAt string
	err := rows.Scan(
		&r.ID, &r.AgentID, &r.TaskID, &r.ProjectID,
		&r.RequestType, &r.Title, &r.Description, &r.Urgency,
		&r.Status, &r.ResponseData, &r.ResponseComment, &r.ResolvedBy,
		&r.TimeoutMinutes, &r.Metadata, &createdAt, &resolvedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan human request row: %w", err)
	}
	if t, parseErr := time.Parse("2006-01-02 15:04:05", createdAt); parseErr == nil {
		r.CreatedAt = t
	}
	if resolvedAt.Valid {
		r.ResolvedAt = &resolvedAt.Time
	}
	return &r, nil
}
