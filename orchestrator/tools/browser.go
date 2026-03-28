package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// BrowserPageProvider abstracts the BrowserManager to avoid circular imports.
// The ratchetplugin package implements this via BrowserManager.
type BrowserPageProvider interface {
	GetPage(agentID string) (*rod.Page, error)
}

// defaultBrowserMaxTextLength is the default cap applied to extracted page text.
const defaultBrowserMaxTextLength = 2000

// BrowserNavigateTool navigates the agent's browser page to a URL and returns
// the page title and a text excerpt.
type BrowserNavigateTool struct {
	Manager BrowserPageProvider
	// MaxTextLength caps the number of characters returned in the page text field.
	// A value of 0 (or negative) falls back to defaultBrowserMaxTextLength (2000).
	MaxTextLength int
}

func (t *BrowserNavigateTool) Name() string { return "browser_navigate" }
func (t *BrowserNavigateTool) Description() string {
	return "Navigate the browser to a URL and return page title and text"
}
func (t *BrowserNavigateTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "URL to navigate to"},
			},
			"required": []string{"url"},
		},
	}
}

func (t *BrowserNavigateTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	url, _ := args["url"].(string)
	if url == "" {
		return nil, fmt.Errorf("url is required")
	}

	agentID, _ := AgentIDFromContext(ctx)
	page, err := t.Manager.GetPage(agentID)
	if err != nil {
		return nil, fmt.Errorf("get browser page: %w", err)
	}

	if err := page.Navigate(url); err != nil {
		return nil, fmt.Errorf("navigate to %s: %w", url, err)
	}

	// Wait for load with timeout
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := page.Context(waitCtx).WaitLoad(); err != nil {
		// Non-fatal: page may have loaded enough even if WaitLoad times out
		_ = err
	}

	// Get page title via JS eval
	titleRes, err := page.Eval(`() => document.title`)
	title := ""
	if err == nil && titleRes != nil {
		title = titleRes.Value.String()
	}

	// Get page text content via JS eval
	textRes, err := page.Eval(`() => document.body ? document.body.innerText : ""`)
	text := ""
	if err == nil && textRes != nil {
		text = textRes.Value.String()
	}
	maxLen := t.MaxTextLength
	if maxLen <= 0 {
		maxLen = defaultBrowserMaxTextLength
	}
	if len(text) > maxLen {
		text = text[:maxLen]
	}

	return map[string]any{
		"title": title,
		"text":  text,
	}, nil
}

// BrowserScreenshotTool captures a screenshot of the agent's current browser page.
type BrowserScreenshotTool struct {
	Manager BrowserPageProvider
}

func (t *BrowserScreenshotTool) Name() string { return "browser_screenshot" }
func (t *BrowserScreenshotTool) Description() string {
	return "Take a screenshot of the current browser page"
}
func (t *BrowserScreenshotTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *BrowserScreenshotTool) Execute(ctx context.Context, _ map[string]any) (any, error) {
	agentID, _ := AgentIDFromContext(ctx)
	page, err := t.Manager.GetPage(agentID)
	if err != nil {
		return nil, fmt.Errorf("get browser page: %w", err)
	}

	png, err := page.Screenshot(false, nil)
	if err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}

	return map[string]any{
		"image_base64": base64.StdEncoding.EncodeToString(png),
	}, nil
}

// BrowserClickTool clicks an element on the agent's current browser page.
type BrowserClickTool struct {
	Manager BrowserPageProvider
}

func (t *BrowserClickTool) Name() string { return "browser_click" }
func (t *BrowserClickTool) Description() string {
	return "Click an element on the current browser page by CSS selector"
}
func (t *BrowserClickTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"selector": map[string]any{"type": "string", "description": "CSS selector of element to click"},
			},
			"required": []string{"selector"},
		},
	}
}

func (t *BrowserClickTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	selector, _ := args["selector"].(string)
	if selector == "" {
		return nil, fmt.Errorf("selector is required")
	}

	agentID, _ := AgentIDFromContext(ctx)
	page, err := t.Manager.GetPage(agentID)
	if err != nil {
		return nil, fmt.Errorf("get browser page: %w", err)
	}

	el, err := page.Element(selector)
	if err != nil {
		return map[string]any{
			"success": false,
			"error":   fmt.Sprintf("element not found: %v", err),
		}, nil
	}

	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return map[string]any{
			"success": false,
			"error":   err.Error(),
		}, nil
	}

	return map[string]any{"success": true}, nil
}

// BrowserExtractTool extracts text and HTML from elements matching a CSS selector.
type BrowserExtractTool struct {
	Manager BrowserPageProvider
}

func (t *BrowserExtractTool) Name() string { return "browser_extract" }
func (t *BrowserExtractTool) Description() string {
	return "Extract text and HTML from elements matching a CSS selector"
}
func (t *BrowserExtractTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"selector": map[string]any{"type": "string", "description": "CSS selector to extract elements from"},
			},
			"required": []string{"selector"},
		},
	}
}

func (t *BrowserExtractTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	selector, _ := args["selector"].(string)
	if selector == "" {
		return nil, fmt.Errorf("selector is required")
	}

	agentID, _ := AgentIDFromContext(ctx)
	page, err := t.Manager.GetPage(agentID)
	if err != nil {
		return nil, fmt.Errorf("get browser page: %w", err)
	}

	els, err := page.Elements(selector)
	if err != nil {
		return nil, fmt.Errorf("query elements: %w", err)
	}

	var elements []map[string]any
	for _, el := range els {
		text, _ := el.Text()
		html, _ := el.HTML()
		elements = append(elements, map[string]any{
			"text": strings.TrimSpace(text),
			"html": html,
		})
	}

	if elements == nil {
		elements = []map[string]any{}
	}

	return map[string]any{"elements": elements}, nil
}

// BrowserFillTool fills an input element on the agent's current browser page.
type BrowserFillTool struct {
	Manager BrowserPageProvider
}

func (t *BrowserFillTool) Name() string { return "browser_fill" }
func (t *BrowserFillTool) Description() string {
	return "Fill an input element on the current browser page"
}
func (t *BrowserFillTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"selector": map[string]any{"type": "string", "description": "CSS selector of the input element"},
				"value":    map[string]any{"type": "string", "description": "Value to fill in"},
			},
			"required": []string{"selector", "value"},
		},
	}
}

func (t *BrowserFillTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	selector, _ := args["selector"].(string)
	value, _ := args["value"].(string)
	if selector == "" {
		return nil, fmt.Errorf("selector is required")
	}

	agentID, _ := AgentIDFromContext(ctx)
	page, err := t.Manager.GetPage(agentID)
	if err != nil {
		return nil, fmt.Errorf("get browser page: %w", err)
	}

	el, err := page.Element(selector)
	if err != nil {
		return map[string]any{
			"success": false,
			"error":   fmt.Sprintf("element not found: %v", err),
		}, nil
	}

	if err := el.Input(value); err != nil {
		return map[string]any{
			"success": false,
			"error":   err.Error(),
		}, nil
	}

	return map[string]any{"success": true}, nil
}
