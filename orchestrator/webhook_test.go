package orchestrator

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupWebhookTestDB creates an in-memory SQLite DB with the webhooks table.
func setupWebhookTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(createWebhooksTable); err != nil {
		t.Fatalf("create webhooks table: %v", err)
	}
	return db
}

// computeHMAC computes sha256=<hex> HMAC.
func computeHMAC(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// computeSlackHMAC computes v0=<hex> HMAC using the correct Slack signing scheme:
// HMAC-SHA256("v0:<timestamp>:<body>", secret).
func computeSlackHMAC(secret string, timestamp string, payload []byte) string {
	baseString := "v0:" + timestamp + ":" + string(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(baseString))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookManagerCreate(t *testing.T) {
	db := setupWebhookTestDB(t)
	defer func() { _ = db.Close() }()

	wm := NewWebhookManager(db, nil)
	ctx := context.Background()

	wh := Webhook{
		Source:       "github",
		Name:         "test-webhook",
		SecretName:   "my-secret",
		Filter:       "issues.opened",
		TaskTemplate: "title: Issue opened\ndescription: {{.payload.title}}",
		Enabled:      true,
	}

	if err := wm.Create(ctx, wh); err != nil {
		t.Fatalf("Create webhook: %v", err)
	}

	hooks, err := wm.List(ctx)
	if err != nil {
		t.Fatalf("List webhooks: %v", err)
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(hooks))
	}
	if hooks[0].Name != "test-webhook" {
		t.Errorf("expected name 'test-webhook', got %q", hooks[0].Name)
	}
	if hooks[0].Source != "github" {
		t.Errorf("expected source 'github', got %q", hooks[0].Source)
	}
	if hooks[0].Filter != "issues.opened" {
		t.Errorf("expected filter 'issues.opened', got %q", hooks[0].Filter)
	}
	if !hooks[0].Enabled {
		t.Error("expected webhook to be enabled")
	}
}

func TestWebhookManagerDelete(t *testing.T) {
	db := setupWebhookTestDB(t)
	defer func() { _ = db.Close() }()

	wm := NewWebhookManager(db, nil)
	ctx := context.Background()

	wh := Webhook{ID: "test-id", Source: "generic", Name: "del-test", Enabled: true}
	if err := wm.Create(ctx, wh); err != nil {
		t.Fatalf("Create webhook: %v", err)
	}

	if err := wm.Delete(ctx, "test-id"); err != nil {
		t.Fatalf("Delete webhook: %v", err)
	}

	hooks, err := wm.List(ctx)
	if err != nil {
		t.Fatalf("List webhooks: %v", err)
	}
	if len(hooks) != 0 {
		t.Errorf("expected 0 webhooks after delete, got %d", len(hooks))
	}
}

func TestWebhookManagerGetBySource(t *testing.T) {
	db := setupWebhookTestDB(t)
	defer func() { _ = db.Close() }()

	wm := NewWebhookManager(db, nil)
	ctx := context.Background()

	hooks := []Webhook{
		{Source: "github", Name: "gh-hook-1", Enabled: true},
		{Source: "github", Name: "gh-hook-2", Enabled: true},
		{Source: "slack", Name: "slack-hook", Enabled: true},
		{Source: "github", Name: "gh-disabled", Enabled: false},
	}
	for _, h := range hooks {
		if err := wm.Create(ctx, h); err != nil {
			t.Fatalf("Create webhook %q: %v", h.Name, err)
		}
	}

	ghHooks, err := wm.GetBySource(ctx, "github")
	if err != nil {
		t.Fatalf("GetBySource github: %v", err)
	}
	// Should only return enabled github hooks
	if len(ghHooks) != 2 {
		t.Errorf("expected 2 enabled github webhooks, got %d", len(ghHooks))
	}

	slackHooks, err := wm.GetBySource(ctx, "slack")
	if err != nil {
		t.Fatalf("GetBySource slack: %v", err)
	}
	if len(slackHooks) != 1 {
		t.Errorf("expected 1 slack webhook, got %d", len(slackHooks))
	}
}

func TestWebhookVerifySignatureGitHub(t *testing.T) {
	wm := NewWebhookManager(nil, nil)
	secret := "my-github-secret"
	payload := []byte(`{"action":"opened","issue":{"title":"test"}}`)

	sig := computeHMAC(secret, payload)

	tests := []struct {
		name string
		sig  string
		want bool
	}{
		{"valid signature", sig, true},
		{"wrong signature", "sha256=deadbeef", false},
		{"missing prefix", hex.EncodeToString([]byte("wrong")), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wm.VerifySignature("github", secret, payload, tc.sig, "")
			if got != tc.want {
				t.Errorf("VerifySignature(%q) = %v, want %v", tc.sig, got, tc.want)
			}
		})
	}
}

func TestWebhookVerifySignatureGeneric(t *testing.T) {
	wm := NewWebhookManager(nil, nil)
	secret := "my-webhook-secret"
	payload := []byte(`{"type":"deploy","env":"production"}`)

	sig := computeHMAC(secret, payload)

	if !wm.VerifySignature("generic", secret, payload, sig, "") {
		t.Error("expected signature verification to pass for generic source")
	}
	if wm.VerifySignature("generic", secret, payload, "sha256=wrong", "") {
		t.Error("expected signature verification to fail with wrong sig")
	}
}

func TestWebhookVerifySignatureSlack(t *testing.T) {
	wm := NewWebhookManager(nil, nil)
	secret := "slack-signing-secret"
	payload := []byte(`{"type":"event_callback","event":{"type":"app_mention"}}`)

	// Use a recent timestamp so replay protection does not reject the request.
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	sig := computeSlackHMAC(secret, timestamp, payload)

	if !wm.VerifySignature("slack", secret, payload, sig, timestamp) {
		t.Error("expected slack signature verification to pass")
	}
	if wm.VerifySignature("slack", secret, payload, "v0=badsig", timestamp) {
		t.Error("expected slack signature verification to fail with bad sig")
	}
}

func TestWebhookVerifySignatureNoSecret(t *testing.T) {
	wm := NewWebhookManager(nil, nil)
	payload := []byte(`{"event":"push"}`)

	// Empty secret should return true (no verification required)
	if !wm.VerifySignature("github", "", payload, "", "") {
		t.Error("expected true when no secret configured")
	}

	// Non-empty secret with empty sig should return false
	if wm.VerifySignature("github", "secret", payload, "", "") {
		t.Error("expected false when secret present but no signature provided")
	}
}

func TestWebhookMatchesFilter(t *testing.T) {
	wm := NewWebhookManager(nil, nil)

	tests := []struct {
		source    string
		eventType string
		filter    string
		want      bool
	}{
		// Empty filter matches everything
		{"github", "push", "", true},
		{"github", "issues.opened", "", true},
		{"github", "", "", true},

		// Exact match
		{"github", "push", "push", true},
		{"github", "issues.opened", "issues.opened", true},

		// Prefix match (filter is prefix of event)
		{"github", "issues.opened", "issues", true},
		{"github", "issues.closed", "issues", true},

		// No match
		{"github", "push", "issues", false},
		{"github", "pull_request.opened", "issues", false},

		// Empty event type with non-empty filter
		{"github", "", "push", false},

		// Case insensitive
		{"github", "PUSH", "push", true},
		{"github", "Issues.Opened", "issues.opened", true},
	}

	for _, tc := range tests {
		t.Run(tc.source+"/"+tc.eventType+"/"+tc.filter, func(t *testing.T) {
			got := wm.MatchesFilter(tc.source, tc.eventType, tc.filter)
			if got != tc.want {
				t.Errorf("MatchesFilter(%q, %q, %q) = %v, want %v",
					tc.source, tc.eventType, tc.filter, got, tc.want)
			}
		})
	}
}

func TestWebhookExtractEventType(t *testing.T) {
	wm := NewWebhookManager(nil, nil)

	tests := []struct {
		name    string
		source  string
		headers map[string]string
		payload map[string]any
		want    string
	}{
		{
			name:    "github push",
			source:  "github",
			headers: map[string]string{"X-GitHub-Event": "push"},
			payload: map[string]any{},
			want:    "push",
		},
		{
			name:    "github issues with action",
			source:  "github",
			headers: map[string]string{"X-GitHub-Event": "issues"},
			payload: map[string]any{"action": "opened"},
			want:    "issues.opened",
		},
		{
			name:    "slack event callback",
			source:  "slack",
			headers: map[string]string{},
			payload: map[string]any{
				"event": map[string]any{"type": "app_mention"},
			},
			want: "app_mention",
		},
		{
			name:    "slack type at top level",
			source:  "slack",
			headers: map[string]string{},
			payload: map[string]any{"type": "url_verification"},
			want:    "url_verification",
		},
		{
			name:    "generic with type field",
			source:  "generic",
			headers: map[string]string{},
			payload: map[string]any{"type": "deploy"},
			want:    "deploy",
		},
		{
			name:    "github no header",
			source:  "github",
			headers: map[string]string{},
			payload: map[string]any{},
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wm.ExtractEventType(tc.source, tc.headers, tc.payload)
			if got != tc.want {
				t.Errorf("ExtractEventType = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWebhookRenderTaskTemplate(t *testing.T) {
	wm := NewWebhookManager(nil, nil)

	tests := []struct {
		name      string
		tmpl      string
		payload   map[string]any
		wantTitle string
		wantDesc  string
		wantErr   bool
	}{
		{
			name:      "empty template uses default",
			tmpl:      "",
			payload:   map[string]any{},
			wantTitle: "Webhook event received",
		},
		{
			name:      "empty template with title in payload",
			tmpl:      "",
			payload:   map[string]any{"title": "My task title"},
			wantTitle: "My task title",
		},
		{
			name: "structured template with title and description",
			tmpl: "title: Issue: {{.payload.issue_title}}\ndescription: Opened by {{.payload.user}}",
			payload: map[string]any{
				"issue_title": "Fix the bug",
				"user":        "alice",
			},
			wantTitle: "Issue: Fix the bug",
			wantDesc:  "Opened by alice",
		},
		{
			name:      "plain template becomes title",
			tmpl:      "Deploy to {{.payload.env}}",
			payload:   map[string]any{"env": "production"},
			wantTitle: "Deploy to production",
		},
		{
			name:    "invalid template returns error",
			tmpl:    "{{.unclosed",
			payload: map[string]any{},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			title, desc, err := wm.RenderTaskTemplate(tc.tmpl, tc.payload)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if title != tc.wantTitle {
				t.Errorf("title = %q, want %q", title, tc.wantTitle)
			}
			if desc != tc.wantDesc {
				t.Errorf("description = %q, want %q", desc, tc.wantDesc)
			}
		})
	}
}

func TestWebhookToMap(t *testing.T) {
	wh := Webhook{
		ID:           "abc-123",
		Source:       "github",
		Name:         "my-webhook",
		SecretName:   "gh-secret",
		Filter:       "issues",
		TaskTemplate: "title: {{.payload.title}}",
		Enabled:      true,
		CreatedAt:    time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
	}

	m := webhookToMap(wh)

	if m["id"] != "abc-123" {
		t.Errorf("id = %v, want 'abc-123'", m["id"])
	}
	if m["source"] != "github" {
		t.Errorf("source = %v, want 'github'", m["source"])
	}
	if m["enabled"] != true {
		t.Errorf("enabled = %v, want true", m["enabled"])
	}
}

func TestWebhookManagerListEmpty(t *testing.T) {
	db := setupWebhookTestDB(t)
	defer func() { _ = db.Close() }()

	wm := NewWebhookManager(db, nil)
	ctx := context.Background()

	hooks, err := wm.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(hooks) != 0 {
		t.Errorf("expected empty list, got %d hooks", len(hooks))
	}
}

func TestWebhookIDAutoGenerated(t *testing.T) {
	db := setupWebhookTestDB(t)
	defer func() { _ = db.Close() }()

	wm := NewWebhookManager(db, nil)
	ctx := context.Background()

	// No ID provided — should be auto-generated
	wh := Webhook{Source: "generic", Name: "auto-id-test", Enabled: true}
	if err := wm.Create(ctx, wh); err != nil {
		t.Fatalf("Create: %v", err)
	}

	hooks, err := wm.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}
	if hooks[0].ID == "" {
		t.Error("expected auto-generated ID, got empty string")
	}
}
