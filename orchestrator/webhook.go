package orchestrator

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/google/uuid"
)

// Webhook represents a configured inbound webhook endpoint.
type Webhook struct {
	ID           string
	Source       string // "github", "slack", "generic"
	Name         string
	SecretName   string // name of secret in vault for HMAC verification
	Filter       string // event filter e.g. "issues.opened", "push"
	TaskTemplate string // Go template for task title/description
	Enabled      bool
	CreatedAt    time.Time
}

// WebhookManager manages webhook configurations and processing logic.
type WebhookManager struct {
	db    *sql.DB
	guard *SecretGuard
}

// NewWebhookManager creates a new WebhookManager.
func NewWebhookManager(db *sql.DB, guard *SecretGuard) *WebhookManager {
	return &WebhookManager{db: db, guard: guard}
}

// Create inserts a new webhook into the database.
func (wm *WebhookManager) Create(ctx context.Context, wh Webhook) error {
	if wh.ID == "" {
		wh.ID = uuid.NewString()
	}
	if wh.Source == "" {
		wh.Source = "generic"
	}
	enabledInt := 1
	if !wh.Enabled {
		enabledInt = 0
	}
	_, err := wm.db.ExecContext(ctx,
		`INSERT INTO webhooks (id, source, name, secret_name, filter, task_template, enabled) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		wh.ID, wh.Source, wh.Name, wh.SecretName, wh.Filter, wh.TaskTemplate, enabledInt,
	)
	return err
}

// Delete removes a webhook by ID.
func (wm *WebhookManager) Delete(ctx context.Context, id string) error {
	_, err := wm.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	return err
}

// List returns all webhooks.
func (wm *WebhookManager) List(ctx context.Context) ([]Webhook, error) {
	rows, err := wm.db.QueryContext(ctx,
		`SELECT id, source, name, secret_name, filter, task_template, enabled, created_at FROM webhooks ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanWebhooks(rows)
}

// GetBySource returns all enabled webhooks matching a source identifier.
func (wm *WebhookManager) GetBySource(ctx context.Context, source string) ([]Webhook, error) {
	rows, err := wm.db.QueryContext(ctx,
		`SELECT id, source, name, secret_name, filter, task_template, enabled, created_at FROM webhooks WHERE source = ? AND enabled = 1 ORDER BY created_at ASC`,
		source,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanWebhooks(rows)
}

// scanWebhooks scans SQL rows into Webhook slices.
func scanWebhooks(rows *sql.Rows) ([]Webhook, error) {
	var result []Webhook
	for rows.Next() {
		var wh Webhook
		var enabledInt int
		if err := rows.Scan(&wh.ID, &wh.Source, &wh.Name, &wh.SecretName, &wh.Filter, &wh.TaskTemplate, &enabledInt, &wh.CreatedAt); err != nil {
			return nil, err
		}
		wh.Enabled = enabledInt != 0
		result = append(result, wh)
	}
	return result, rows.Err()
}

// slackMaxTimestampAge is the maximum age of a Slack request timestamp before
// it is rejected to prevent replay attacks.
const slackMaxTimestampAge = 5 * time.Minute

// VerifySignature verifies the HMAC signature of a webhook payload.
// Returns true if the signature matches or if no secret is configured (SecretName is empty).
//
// Signature schemes:
//   - github:  X-Hub-Signature-256: sha256=<hex>
//   - slack:   X-Slack-Signature:   v0=<hex>  (message: "v0:<timestamp>:<body>")
//   - generic: X-Webhook-Signature: sha256=<hex>
//
// For Slack, timestamp is the value of the X-Slack-Request-Timestamp header and is
// required to construct the correct base string and enforce replay protection.
func (wm *WebhookManager) VerifySignature(source string, secret string, payload []byte, signature string, timestamp string) bool {
	if secret == "" || signature == "" {
		// No secret configured — accept without verification
		return secret == ""
	}

	switch source {
	case "slack":
		// Slack requires a valid timestamp to prevent replay attacks.
		if timestamp == "" {
			return false
		}
		// Parse Unix timestamp and reject requests older than 5 minutes.
		var tsSeconds int64
		if _, err := fmt.Sscanf(timestamp, "%d", &tsSeconds); err != nil {
			return false
		}
		requestTime := time.Unix(tsSeconds, 0)
		if time.Since(requestTime) > slackMaxTimestampAge {
			return false
		}
		// Slack signing: HMAC-SHA256("v0:<timestamp>:<body>", secret), prefix "v0="
		baseString := fmt.Sprintf("v0:%s:%s", timestamp, string(payload))
		sig := strings.TrimPrefix(signature, "v0=")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(baseString))
		expected := hex.EncodeToString(mac.Sum(nil))
		return hmac.Equal([]byte(sig), []byte(expected))

	default: // "github", "generic"
		// Format: sha256=<hex>
		sig := strings.TrimPrefix(signature, "sha256=")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		expected := hex.EncodeToString(mac.Sum(nil))
		return hmac.Equal([]byte(sig), []byte(expected))
	}
}

// MatchesFilter returns true if the event type matches the webhook filter.
// An empty filter matches everything.
// Filter format: "event.action" (e.g. "issues.opened", "push") or just "event" (e.g. "push").
func (wm *WebhookManager) MatchesFilter(source string, eventType string, filter string) bool {
	if filter == "" {
		return true
	}
	if eventType == "" {
		return false
	}
	return strings.EqualFold(eventType, filter) ||
		strings.HasPrefix(strings.ToLower(eventType), strings.ToLower(filter)+".")
}

// ExtractEventType extracts the event type from request headers or payload.
func (wm *WebhookManager) ExtractEventType(source string, headers map[string]string, payload map[string]any) string {
	switch source {
	case "github":
		// GitHub sends event type in X-GitHub-Event header
		if v, ok := headers["X-GitHub-Event"]; ok {
			// Combine with action if available (e.g. "issues.opened")
			if action, ok := extractNestedString(payload, "action"); ok && action != "" {
				return v + "." + action
			}
			return v
		}
	case "slack":
		// Slack sends event type in payload.event.type
		if event, ok := payload["event"].(map[string]any); ok {
			if t, ok := event["type"].(string); ok {
				return t
			}
		}
		// Challenge/callback_id at top level
		if t, ok := payload["type"].(string); ok {
			return t
		}
	default: // generic
		if t, ok := payload["type"].(string); ok {
			return t
		}
	}
	return ""
}

// extractNestedString extracts a string field from a nested map.
func extractNestedString(m map[string]any, key string) (string, bool) {
	if v, ok := m[key].(string); ok {
		return v, true
	}
	return "", false
}

// RenderTaskTemplate renders the Go template with the webhook payload as data.
// The template can use {{.title}} and {{.description}} fields or any payload fields.
// Returns title, description, and any rendering error.
//
// Example template:
//
//	title: "GitHub issue: {{.payload.title}}"
//	description: "Opened by {{.payload.user.login}}"
func (wm *WebhookManager) RenderTaskTemplate(tmpl string, payload map[string]any) (title string, description string, err error) {
	if tmpl == "" {
		// Default template
		title = "Webhook event received"
		description = ""
		if t, ok := payload["title"].(string); ok {
			title = t
		}
		return title, description, nil
	}

	// Parse and execute the template
	t, err := template.New("webhook-task").Parse(tmpl)
	if err != nil {
		return "", "", fmt.Errorf("parse task template: %w", err)
	}

	data := map[string]any{
		"payload": payload,
	}

	// If template contains "title:" and "description:" sections, parse them
	if strings.Contains(tmpl, "title:") {
		// Try structured template with title/description sections
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return "", "", fmt.Errorf("render task template: %w", err)
		}
		rendered := buf.String()

		// Parse lines for title: and description:
		for _, line := range strings.Split(rendered, "\n") {
			line = strings.TrimSpace(line)
			if after, found := strings.CutPrefix(line, "title:"); found {
				title = strings.TrimSpace(after)
			} else if after, found := strings.CutPrefix(line, "description:"); found {
				description = strings.TrimSpace(after)
			}
		}
		if title == "" {
			title = "Webhook event received"
		}
		return title, description, nil
	}

	// Plain template — treat the whole output as the title
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", "", fmt.Errorf("render task template: %w", err)
	}
	title = strings.TrimSpace(buf.String())
	if title == "" {
		title = "Webhook event received"
	}
	return title, description, nil
}

// webhookToMap converts a Webhook to a map for JSON responses.
func webhookToMap(wh Webhook) map[string]any {
	return map[string]any{
		"id":            wh.ID,
		"source":        wh.Source,
		"name":          wh.Name,
		"secret_name":   wh.SecretName,
		"filter":        wh.Filter,
		"task_template": wh.TaskTemplate,
		"enabled":       wh.Enabled,
		"created_at":    wh.CreatedAt.Format(time.RFC3339),
	}
}

// marshalJSON serialises a value to a JSON string (best-effort).
func marshalJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
