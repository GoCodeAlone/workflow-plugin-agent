package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// MCPReloadStep triggers a hot-reload of MCP server connections from the database.
type MCPReloadStep struct {
	name string
	app  modular.Application
}

func (s *MCPReloadStep) Name() string { return s.name }

func (s *MCPReloadStep) Execute(ctx context.Context, _ *module.PipelineContext) (*module.StepResult, error) {
	// Get DB
	var db *sql.DB
	if svc, ok := s.app.SvcRegistry()["ratchet-db"]; ok {
		if dbp, ok := svc.(module.DBProvider); ok {
			db = dbp.DB()
		}
	}
	if db == nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "database not available",
			},
		}, nil
	}

	// Get MCP client module
	svc, ok := s.app.SvcRegistry()["ratchet-mcp-client"]
	if !ok {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "MCP client module not available",
			},
		}, nil
	}

	mcpMod, ok := svc.(*MCPClientModule)
	if !ok {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "MCP client module type mismatch",
			},
		}, nil
	}

	// Load MCP server configs from DB
	configs, err := loadMCPServersFromDB(db)
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "failed to load MCP server configs from database",
			},
		}, nil
	}

	// Reload
	reloaded, errors := mcpMod.ReloadServers(configs)

	result := map[string]any{
		"success":  len(errors) == 0,
		"reloaded": reloaded,
		"message":  fmt.Sprintf("reloaded %d MCP servers", reloaded),
	}
	if len(errors) > 0 {
		result["errors"] = errors
	}

	return &module.StepResult{Output: result}, nil
}

// loadMCPServersFromDB reads active MCP server configs from the database.
func loadMCPServersFromDB(db *sql.DB) ([]mcpServerConfig, error) {
	rows, err := db.Query(`SELECT name, command, args FROM mcp_servers WHERE status = 'active' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var configs []mcpServerConfig
	for rows.Next() {
		var cfg mcpServerConfig
		var argsJSON string
		if err := rows.Scan(&cfg.Name, &cfg.Command, &argsJSON); err != nil {
			continue
		}
		// Parse args from JSON array
		if argsJSON != "" && argsJSON != "[]" {
			_ = json.Unmarshal([]byte(argsJSON), &cfg.Args)
		}
		configs = append(configs, cfg)
	}
	return configs, rows.Err()
}

func newMCPReloadFactory() plugin.StepFactory {
	return func(name string, _ map[string]any, app modular.Application) (any, error) {
		return &MCPReloadStep{
			name: name,
			app:  app,
		}, nil
	}
}
