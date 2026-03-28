package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/google/uuid"
)

// WebhookProcessStep processes an inbound webhook request.
// It:
//  1. Extracts the webhook source from the URL path parameter (:source / {source})
//  2. Reads the raw request body
//  3. Looks up matching webhook configs from the DB
//  4. Verifies HMAC signature if a secret is configured
//  5. Applies event type filters
//  6. Creates a task using the webhook's task template
//  7. Returns the created task ID
type WebhookProcessStep struct {
	name string
	app  modular.Application
}

func (s *WebhookProcessStep) Name() string { return s.name }

// Execute processes the inbound webhook and auto-creates a task.
func (s *WebhookProcessStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	// --- 1. Extract source from path params ---
	source := extractSourceFromContext(pc)
	if source == "" {
		return nil, fmt.Errorf("webhook_process step %q: source path parameter is required", s.name)
	}

	// --- 2. Read raw request body and headers ---
	rawBody, headers, err := extractWebhookRequest(pc)
	if err != nil {
		return nil, fmt.Errorf("webhook_process step %q: read request: %w", s.name, err)
	}

	// --- 3. Parse body as JSON ---
	var payload map[string]any
	if len(rawBody) > 0 {
		if jsonErr := json.Unmarshal(rawBody, &payload); jsonErr != nil {
			payload = map[string]any{"raw": string(rawBody)}
		}
	}
	if payload == nil {
		payload = map[string]any{}
	}

	// --- 4. Look up webhook manager ---
	wm := s.lookupWebhookManager()
	if wm == nil {
		return nil, fmt.Errorf("webhook_process step %q: webhook manager not available", s.name)
	}

	// --- 5. Find matching webhooks by source ---
	webhooks, err := wm.GetBySource(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("webhook_process step %q: lookup webhooks: %w", s.name, err)
	}
	if len(webhooks) == 0 {
		return &module.StepResult{
			Output: map[string]any{
				"processed": false,
				"reason":    "no matching webhook configuration found",
				"source":    source,
			},
		}, nil
	}

	// --- 6. Extract event type for filtering ---
	eventType := wm.ExtractEventType(source, headers, payload)

	// --- 7. Process each matching webhook ---
	var createdTaskIDs []string
	for _, wh := range webhooks {
		// Signature verification
		if wh.SecretName != "" {
			secret := s.resolveSecret(ctx, wh.SecretName)
			sig := extractSignatureHeader(source, headers)
			timestamp := headers["X-Slack-Request-Timestamp"]
			if !wm.VerifySignature(source, secret, rawBody, sig, timestamp) {
				// Signature mismatch — skip this webhook but don't fail the request
				continue
			}
		}

		// Event filter
		if !wm.MatchesFilter(source, eventType, wh.Filter) {
			continue
		}

		// Render task template
		title, description, tmplErr := wm.RenderTaskTemplate(wh.TaskTemplate, payload)
		if tmplErr != nil {
			// Log but don't fail — use a default title
			title = fmt.Sprintf("Webhook event from %s", source)
			description = ""
		}

		// Create task in DB
		taskID, createErr := s.createTask(ctx, title, description, source, wh.ID)
		if createErr != nil {
			return nil, fmt.Errorf("webhook_process step %q: create task: %w", s.name, createErr)
		}
		createdTaskIDs = append(createdTaskIDs, taskID)
	}

	return &module.StepResult{
		Output: map[string]any{
			"processed":        true,
			"source":           source,
			"event_type":       eventType,
			"tasks_created":    len(createdTaskIDs),
			"task_ids":         createdTaskIDs,
			"webhooks_matched": len(createdTaskIDs),
		},
	}, nil
}

// lookupWebhookManager retrieves the WebhookManager from the service registry.
func (s *WebhookProcessStep) lookupWebhookManager() *WebhookManager {
	if svc, ok := s.app.SvcRegistry()["ratchet-webhook-manager"]; ok {
		if wm, ok := svc.(*WebhookManager); ok {
			return wm
		}
	}
	return nil
}

// resolveSecret retrieves a secret value from the SecretGuard.
func (s *WebhookProcessStep) resolveSecret(ctx context.Context, secretName string) string {
	if svc, ok := s.app.SvcRegistry()["ratchet-secret-guard"]; ok {
		if guard, ok := svc.(*SecretGuard); ok {
			if val, err := guard.Provider().Get(ctx, secretName); err == nil {
				return val
			}
		}
	}
	return ""
}

// createTask inserts a new task record into the database.
func (s *WebhookProcessStep) createTask(ctx context.Context, title, description, source, webhookID string) (string, error) {
	svc, ok := s.app.SvcRegistry()["ratchet-db"]
	if !ok {
		return "", fmt.Errorf("database service 'ratchet-db' not found")
	}
	dbProvider, ok := svc.(module.DBProvider)
	if !ok {
		return "", fmt.Errorf("'ratchet-db' does not implement DBProvider")
	}
	sqlDB := dbProvider.DB()
	if sqlDB == nil {
		return "", fmt.Errorf("database connection is nil")
	}

	taskID := uuid.NewString()
	metadata := marshalJSON(map[string]any{
		"webhook_source": source,
		"webhook_id":     webhookID,
	})

	_, err := sqlDB.ExecContext(ctx,
		`INSERT INTO tasks (id, title, description, status, priority, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, 'pending', 1, ?, datetime('now'), datetime('now'))`,
		taskID, title, description, metadata,
	)
	if err != nil {
		return "", err
	}
	return taskID, nil
}

// extractSourceFromContext gets the :source / {source} path param from pipeline context.
func extractSourceFromContext(pc *module.PipelineContext) string {
	// Check the current step output path_params (set by step.request_parse)
	for _, stepName := range []string{"parse-request", "parse-webhook"} {
		if step, ok := pc.StepOutputs[stepName]; ok {
			if pp, ok := step["path_params"].(map[string]any); ok {
				if src, ok := pp["source"].(string); ok && src != "" {
					return src
				}
			}
		}
	}
	// Check current step context directly
	if pp, ok := pc.Current["path_params"].(map[string]any); ok {
		if src, ok := pp["source"].(string); ok && src != "" {
			return src
		}
	}
	// Fallback: extract from HTTP request path
	if req, ok := pc.Metadata["_http_request"].(*http.Request); ok {
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		// Path pattern: /api/webhooks/receive/{source}
		for i, p := range parts {
			if p == "receive" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return ""
}

// extractWebhookRequest reads the raw request body and relevant headers.
func extractWebhookRequest(pc *module.PipelineContext) ([]byte, map[string]string, error) {
	headers := make(map[string]string)

	req, ok := pc.Metadata["_http_request"].(*http.Request)
	if !ok || req == nil {
		// No HTTP request in context — check if body was pre-parsed
		if body, ok := pc.TriggerData["body"].(map[string]any); ok {
			b, _ := json.Marshal(body)
			return b, headers, nil
		}
		return nil, headers, nil
	}

	// Extract relevant headers
	for _, h := range []string{
		"X-GitHub-Event",
		"X-Hub-Signature-256",
		"X-Slack-Signature",
		"X-Slack-Request-Timestamp",
		"X-Webhook-Signature",
		"Content-Type",
	} {
		if v := req.Header.Get(h); v != "" {
			headers[h] = v
		}
	}

	// Read body
	if req.Body == nil {
		return nil, headers, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, headers, fmt.Errorf("read body: %w", err)
	}
	return body, headers, nil
}

// extractSignatureHeader returns the signature header value for the given source.
func extractSignatureHeader(source string, headers map[string]string) string {
	switch source {
	case "github":
		return headers["X-Hub-Signature-256"]
	case "slack":
		return headers["X-Slack-Signature"]
	default:
		return headers["X-Webhook-Signature"]
	}
}

// newWebhookProcessStepFactory returns a plugin.StepFactory for "step.webhook_process".
func newWebhookProcessStepFactory() plugin.StepFactory {
	return func(name string, _ map[string]any, app modular.Application) (any, error) {
		return &WebhookProcessStep{
			name: name,
			app:  app,
		}, nil
	}
}
