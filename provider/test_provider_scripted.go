package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ScriptedStep defines a single scripted response in a test scenario.
type ScriptedStep struct {
	Content   string              `yaml:"content" json:"content"`
	ToolCalls []ToolCall `yaml:"tool_calls,omitempty" json:"tool_calls,omitempty"`
	Error     string              `yaml:"error,omitempty" json:"error,omitempty"`
	Delay     time.Duration       `yaml:"delay,omitempty" json:"delay,omitempty"`
}

// ScriptedScenario is a named sequence of steps loadable from YAML.
type ScriptedScenario struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	Steps       []ScriptedStep `yaml:"steps" json:"steps"`
	Loop        bool           `yaml:"loop,omitempty" json:"loop,omitempty"`
}

// LoadScenario reads a ScriptedScenario from a YAML file.
func LoadScenario(path string) (*ScriptedScenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load scenario %q: %w", path, err)
	}
	var scenario ScriptedScenario
	if err := yaml.Unmarshal(data, &scenario); err != nil {
		return nil, fmt.Errorf("parse scenario %q: %w", path, err)
	}
	if len(scenario.Steps) == 0 {
		return nil, fmt.Errorf("scenario %q has no steps", path)
	}
	return &scenario, nil
}

// ScriptedSource returns responses from a pre-defined sequence of steps.
// It is safe for concurrent use.
type ScriptedSource struct {
	steps []ScriptedStep
	loop  bool
	idx   int
	mu    sync.Mutex
}

// NewScriptedSource creates a ScriptedSource from the given steps.
// If loop is true, steps cycle indefinitely; otherwise GetResponse returns
// an error when all steps are exhausted.
func NewScriptedSource(steps []ScriptedStep, loop bool) *ScriptedSource {
	return &ScriptedSource{steps: steps, loop: loop}
}

// NewScriptedSourceFromScenario creates a ScriptedSource from a loaded scenario.
func NewScriptedSourceFromScenario(scenario *ScriptedScenario) *ScriptedSource {
	return NewScriptedSource(scenario.Steps, scenario.Loop)
}

// GetResponse implements ResponseSource.
func (s *ScriptedSource) GetResponse(ctx context.Context, interaction Interaction) (*InteractionResponse, error) {
	// Detect summarization requests and auto-respond without consuming a step.
	if isSummarizationRequest(interaction.Messages) {
		return &InteractionResponse{
			Content: "Summary: The agent has been working on the assigned task. Key actions and results have been recorded.",
		}, nil
	}

	s.mu.Lock()
	if s.idx >= len(s.steps) {
		if !s.loop {
			s.mu.Unlock()
			return nil, fmt.Errorf("scripted source exhausted: all %d steps consumed", len(s.steps))
		}
		s.idx = 0
	}
	step := s.steps[s.idx]
	s.idx++
	s.mu.Unlock()

	// Respect delay
	if step.Delay > 0 {
		select {
		case <-time.After(step.Delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return &InteractionResponse{
		Content:   step.Content,
		ToolCalls: step.ToolCalls,
		Error:     step.Error,
	}, nil
}

// Remaining returns how many unconsumed steps remain.
func (s *ScriptedSource) Remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	rem := len(s.steps) - s.idx
	if rem < 0 {
		return 0
	}
	return rem
}

// isSummarizationRequest checks if the messages look like a context compaction
// summarization request rather than a normal agent interaction.
func isSummarizationRequest(messages []Message) bool {
	if len(messages) < 1 {
		return false
	}
	for _, m := range messages {
		if m.Role == RoleSystem {
			lower := strings.ToLower(m.Content)
			if strings.Contains(lower, "summariser") || strings.Contains(lower, "summarizer") ||
				strings.Contains(lower, "precise summar") {
				return true
			}
		}
	}
	return false
}
