package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// MemoryEntryResult is a minimal view of a memory entry returned by tools.
type MemoryEntryResult struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Category  string    `json:"category"`
	CreatedAt time.Time `json:"created_at"`
}

// MemoryEntry is the interface that memory store entries must satisfy.
type MemoryEntry interface {
	GetID() string
	GetContent() string
	GetCategory() string
	GetCreatedAt() time.Time
}

// MemoryStoreSearcher can search for agent memories.
type MemoryStoreSearcher interface {
	SearchMemory(ctx context.Context, agentID, query string, limit int) ([]MemoryEntryResult, error)
}

// MemoryStoreSaver can persist a memory entry.
type MemoryStoreSaver interface {
	SaveMemory(ctx context.Context, agentID, content, category string) error
}

// MemorySearchTool searches an agent's persistent memory.
type MemorySearchTool struct {
	Store   MemoryStoreSearcher
	AgentID string // fallback if not in context
}

func (t *MemorySearchTool) Name() string        { return "memory_search" }
func (t *MemorySearchTool) Description() string { return "Search persistent agent memory" }
func (t *MemorySearchTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query to find relevant memories",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results to return (default 5)",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (t *MemorySearchTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	limit := 5
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	agentID, ok := AgentIDFromContext(ctx)
	if !ok || agentID == "" {
		agentID = t.AgentID
	}
	if agentID == "" {
		return nil, fmt.Errorf("agent ID not available")
	}
	if t.Store == nil {
		return nil, fmt.Errorf("memory store not initialized")
	}

	results, err := t.Store.SearchMemory(ctx, agentID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("memory search: %w", err)
	}

	return map[string]any{
		"results": results,
		"count":   len(results),
	}, nil
}

// MemorySaveTool saves a new entry to an agent's persistent memory.
type MemorySaveTool struct {
	Store   MemoryStoreSaver
	AgentID string // fallback if not in context
}

func (t *MemorySaveTool) Name() string        { return "memory_save" }
func (t *MemorySaveTool) Description() string { return "Save a fact or decision to persistent memory" }
func (t *MemorySaveTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "Content to remember",
				},
				"category": map[string]any{
					"type":        "string",
					"description": "Category (e.g., decision, fact, preference; default: general)",
				},
			},
			"required": []string{"content"},
		},
	}
}

func (t *MemorySaveTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	content, _ := args["content"].(string)
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}
	category, _ := args["category"].(string)
	if category == "" {
		category = "general"
	}

	agentID, ok := AgentIDFromContext(ctx)
	if !ok || agentID == "" {
		agentID = t.AgentID
	}
	if agentID == "" {
		return nil, fmt.Errorf("agent ID not available")
	}
	if t.Store == nil {
		return nil, fmt.Errorf("memory store not initialized")
	}

	if err := t.Store.SaveMemory(ctx, agentID, content, category); err != nil {
		return nil, fmt.Errorf("memory save: %w", err)
	}

	return map[string]any{"saved": true, "category": category}, nil
}
