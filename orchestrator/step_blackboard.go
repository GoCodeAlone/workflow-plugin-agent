package orchestrator

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// BlackboardPostStep posts an artifact to the Blackboard.
// Config keys: phase, artifact_type, agent_id (all optional; fallback to pc.Current).
type BlackboardPostStep struct {
	name         string
	phase        string
	artifactType string
	agentID      string
	app          modular.Application
}

func (s *BlackboardPostStep) Name() string { return s.name }

func (s *BlackboardPostStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	bb := s.blackboard()
	if bb == nil {
		return nil, fmt.Errorf("blackboard_post step %q: blackboard not available", s.name)
	}

	phase := s.phase
	if phase == "" {
		phase = extractString(pc.Current, "phase", "")
	}
	artifactType := s.artifactType
	if artifactType == "" {
		artifactType = extractString(pc.Current, "artifact_type", "")
	}
	agentID := s.agentID
	if agentID == "" {
		agentID = extractString(pc.Current, "agent_id", "")
	}

	// Content: use "content" key from current data if present, otherwise full current data
	content, _ := pc.Current["content"].(map[string]any)
	if content == nil {
		content = pc.Current
	}

	// Tags: optional list from current data
	var tags []string
	if t, ok := pc.Current["tags"].([]any); ok {
		for _, v := range t {
			if s, ok := v.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	art := Artifact{
		Phase:   phase,
		AgentID: agentID,
		Type:    artifactType,
		Content: content,
		Tags:    tags,
	}

	if err := bb.Post(ctx, art); err != nil {
		return nil, fmt.Errorf("blackboard_post step %q: %w", s.name, err)
	}

	return &module.StepResult{
		Output: map[string]any{
			"id":            art.ID,
			"phase":         art.Phase,
			"artifact_type": art.Type,
			"success":       true,
		},
	}, nil
}

// blackboard returns the Blackboard from the service registry, or nil.
func (s *BlackboardPostStep) blackboard() *Blackboard {
	if svc, ok := s.app.SvcRegistry()["ratchet-blackboard"]; ok {
		if bb, ok := svc.(*Blackboard); ok {
			return bb
		}
	}
	return nil
}

// newBlackboardPostFactory returns a plugin.StepFactory for "step.blackboard_post".
func newBlackboardPostFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		phase, _ := cfg["phase"].(string)
		artifactType, _ := cfg["artifact_type"].(string)
		agentID, _ := cfg["agent_id"].(string)
		return &BlackboardPostStep{
			name:         name,
			phase:        phase,
			artifactType: artifactType,
			agentID:      agentID,
			app:          app,
		}, nil
	}
}

// BlackboardReadStep reads artifacts from the Blackboard and returns them in step output.
// Config keys: phase, artifact_type (optional; fallback to pc.Current), latest_only (bool).
type BlackboardReadStep struct {
	name         string
	phase        string
	artifactType string
	latestOnly   bool
	app          modular.Application
}

func (s *BlackboardReadStep) Name() string { return s.name }

func (s *BlackboardReadStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	bb := s.blackboard()
	if bb == nil {
		return nil, fmt.Errorf("blackboard_read step %q: blackboard not available", s.name)
	}

	phase := s.phase
	if phase == "" {
		phase = extractString(pc.Current, "phase", "")
	}
	artifactType := s.artifactType
	if artifactType == "" {
		artifactType = extractString(pc.Current, "artifact_type", "")
	}

	if s.latestOnly {
		art, err := bb.ReadLatest(ctx, phase)
		if err != nil {
			return nil, fmt.Errorf("blackboard_read step %q: %w", s.name, err)
		}
		var artOut map[string]any
		if art != nil {
			artOut = artifactToMap(*art)
		}
		return &module.StepResult{
			Output: map[string]any{
				"artifact": artOut,
				"found":    art != nil,
			},
		}, nil
	}

	artifacts, err := bb.Read(ctx, phase, artifactType)
	if err != nil {
		return nil, fmt.Errorf("blackboard_read step %q: %w", s.name, err)
	}

	out := make([]map[string]any, 0, len(artifacts))
	for _, a := range artifacts {
		out = append(out, artifactToMap(a))
	}

	return &module.StepResult{
		Output: map[string]any{
			"artifacts": out,
			"count":     len(out),
		},
	}, nil
}

// blackboard returns the Blackboard from the service registry, or nil.
func (s *BlackboardReadStep) blackboard() *Blackboard {
	if svc, ok := s.app.SvcRegistry()["ratchet-blackboard"]; ok {
		if bb, ok := svc.(*Blackboard); ok {
			return bb
		}
	}
	return nil
}

// artifactToMap converts an Artifact to a plain map for step output.
func artifactToMap(a Artifact) map[string]any {
	return map[string]any{
		"id":            a.ID,
		"phase":         a.Phase,
		"agent_id":      a.AgentID,
		"artifact_type": a.Type,
		"content":       a.Content,
		"tags":          a.Tags,
		"created_at":    a.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

// newBlackboardReadFactory returns a plugin.StepFactory for "step.blackboard_read".
func newBlackboardReadFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		phase, _ := cfg["phase"].(string)
		artifactType, _ := cfg["artifact_type"].(string)
		latestOnly, _ := cfg["latest_only"].(bool)
		return &BlackboardReadStep{
			name:         name,
			phase:        phase,
			artifactType: artifactType,
			latestOnly:   latestOnly,
			app:          app,
		}, nil
	}
}
