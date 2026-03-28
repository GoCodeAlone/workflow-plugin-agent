package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/iterator"
	googleoption "google.golang.org/api/option"
)

const (
	defaultGeminiModel     = "gemini-2.0-flash"
	defaultGeminiMaxTokens = 4096
)

// GeminiConfig holds configuration for the Google Gemini provider.
type GeminiConfig struct {
	APIKey     string
	Model      string
	MaxTokens  int
	HTTPClient *http.Client
}

// GeminiProvider implements Provider using the Google Gemini API.
type GeminiProvider struct {
	config GeminiConfig
}

// NewGeminiProvider creates a new Gemini provider. Returns an error if no API key is provided.
func NewGeminiProvider(cfg GeminiConfig) (*GeminiProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("gemini: APIKey is required")
	}
	if cfg.Model == "" {
		cfg.Model = defaultGeminiModel
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultGeminiMaxTokens
	}
	return &GeminiProvider{config: cfg}, nil
}

func (p *GeminiProvider) Name() string { return "gemini" }

func (p *GeminiProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "gemini",
		DisplayName: "Google Gemini",
		Description: "Uses the Google Gemini API with an API key from Google AI Studio.",
		DocsURL:     "https://ai.google.dev/gemini-api/docs/api-key",
		ServerSafe:  true,
	}
}

func (p *GeminiProvider) newGenaiClient(ctx context.Context) (*genai.Client, error) {
	opts := []googleoption.ClientOption{
		googleoption.WithAPIKey(p.config.APIKey),
	}
	if p.config.HTTPClient != nil {
		opts = append(opts, googleoption.WithHTTPClient(p.config.HTTPClient))
	}
	return genai.NewClient(ctx, opts...)
}

func (p *GeminiProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	client, err := p.newGenaiClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("gemini: create client: %w", err)
	}
	defer client.Close()

	model := client.GenerativeModel(p.config.Model)
	maxOut := int32(p.config.MaxTokens)
	model.MaxOutputTokens = &maxOut
	if len(tools) > 0 {
		model.Tools = toGeminiTools(tools)
	}

	contents, systemInstruction := toGeminiContents(messages)
	if systemInstruction != nil {
		model.SystemInstruction = systemInstruction
	}

	resp, err := model.GenerateContent(ctx, contents...)
	if err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}
	return fromGeminiResponse(resp), nil
}

func (p *GeminiProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	client, err := p.newGenaiClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("gemini: create client: %w", err)
	}

	model := client.GenerativeModel(p.config.Model)
	maxOut := int32(p.config.MaxTokens)
	model.MaxOutputTokens = &maxOut
	if len(tools) > 0 {
		model.Tools = toGeminiTools(tools)
	}

	contents, systemInstruction := toGeminiContents(messages)
	if systemInstruction != nil {
		model.SystemInstruction = systemInstruction
	}

	iter := model.GenerateContentStream(ctx, contents...)
	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		defer client.Close()
		send := func(ev StreamEvent) bool {
			select {
			case ch <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			resp, err := iter.Next()
			if err == iterator.Done {
				send(StreamEvent{Type: "done"})
				return
			}
			if err != nil {
				send(StreamEvent{Type: "error", Error: err.Error()})
				return
			}
			for _, cand := range resp.Candidates {
				if cand.Content == nil {
					continue
				}
				for _, part := range cand.Content.Parts {
					switch v := part.(type) {
					case genai.Text:
						if v != "" {
							if !send(StreamEvent{Type: "text", Text: string(v)}) {
								return
							}
						}
					case genai.FunctionCall:
						if !send(StreamEvent{Type: "tool_call", Tool: &ToolCall{
							ID:        v.Name,
							Name:      v.Name,
							Arguments: v.Args,
						}}) {
							return
						}
					}
				}
			}
		}
	}()
	return ch, nil
}

// toGeminiContents converts provider messages to Gemini content parts.
// System messages are returned separately as they set model.SystemInstruction.
func toGeminiContents(messages []Message) ([]genai.Part, *genai.Content) {
	var parts []genai.Part
	var systemInstruction *genai.Content

	for _, msg := range messages {
		switch msg.Role {
		case RoleSystem:
			systemInstruction = &genai.Content{
				Parts: []genai.Part{genai.Text(msg.Content)},
				Role:  "user",
			}
		case RoleUser:
			parts = append(parts, genai.Text(msg.Content))
		case RoleAssistant:
			parts = append(parts, genai.Text(msg.Content))
		case RoleTool:
			// Tool results are passed as FunctionResponse parts.
			var args map[string]any
			_ = json.Unmarshal([]byte(msg.Content), &args)
			if args == nil {
				args = map[string]any{"result": msg.Content}
			}
			parts = append(parts, genai.FunctionResponse{
				Name:     msg.ToolCallID,
				Response: args,
			})
		}
	}
	return parts, systemInstruction
}

// toGeminiTools converts provider tool definitions to Gemini Tool structs.
func toGeminiTools(tools []ToolDef) []*genai.Tool {
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		decls = append(decls, &genai.FunctionDeclaration{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  toGeminiSchema(t.Parameters),
		})
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

// toGeminiSchema converts a JSON Schema map to a genai.Schema.
func toGeminiSchema(params map[string]any) *genai.Schema {
	if params == nil {
		return nil
	}
	schema := &genai.Schema{Type: genai.TypeObject}
	if props, ok := params["properties"].(map[string]any); ok {
		schema.Properties = make(map[string]*genai.Schema, len(props))
		for name, val := range props {
			if propMap, ok := val.(map[string]any); ok {
				schema.Properties[name] = toGeminiSchemaProp(propMap)
			}
		}
	}
	if req, ok := params["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				schema.Required = append(schema.Required, s)
			}
		}
	}
	return schema
}

func toGeminiSchemaProp(m map[string]any) *genai.Schema {
	s := &genai.Schema{}
	if t, ok := m["type"].(string); ok {
		switch t {
		case "string":
			s.Type = genai.TypeString
		case "number":
			s.Type = genai.TypeNumber
		case "integer":
			s.Type = genai.TypeInteger
		case "boolean":
			s.Type = genai.TypeBoolean
		case "array":
			s.Type = genai.TypeArray
		case "object":
			s.Type = genai.TypeObject
		}
	}
	if desc, ok := m["description"].(string); ok {
		s.Description = desc
	}
	return s
}

// fromGeminiResponse extracts a provider Response from a Gemini GenerateContentResponse.
func fromGeminiResponse(resp *genai.GenerateContentResponse) *Response {
	result := &Response{}
	if resp.UsageMetadata != nil {
		result.Usage = Usage{
			InputTokens:  int(resp.UsageMetadata.PromptTokenCount),
			OutputTokens: int(resp.UsageMetadata.CandidatesTokenCount),
		}
	}
	if len(resp.Candidates) == 0 {
		return result
	}
	cand := resp.Candidates[0]
	if cand.Content == nil {
		return result
	}
	for _, part := range cand.Content.Parts {
		switch v := part.(type) {
		case genai.Text:
			result.Content += string(v)
		case genai.FunctionCall:
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        v.Name,
				Name:      v.Name,
				Arguments: v.Args,
			})
		}
	}
	return result
}

// listGeminiModels lists available Gemini models using the genai SDK.
func listGeminiModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	client, err := genai.NewClient(ctx, googleoption.WithAPIKey(apiKey))
	if err != nil {
		return geminiFallbackModels(), nil
	}
	defer client.Close()

	iter := client.ListModels(ctx)
	var models []ModelInfo
	for {
		m, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return geminiFallbackModels(), nil
		}
		models = append(models, ModelInfo{
			ID:   m.Name,
			Name: m.DisplayName,
		})
	}
	if len(models) == 0 {
		return geminiFallbackModels(), nil
	}
	return models, nil
}

func geminiFallbackModels() []ModelInfo {
	return []ModelInfo{
		{ID: "gemini-2.5-pro-preview-03-25", Name: "Gemini 2.5 Pro Preview"},
		{ID: "gemini-2.0-flash", Name: "Gemini 2.0 Flash"},
		{ID: "gemini-2.0-flash-lite", Name: "Gemini 2.0 Flash-Lite"},
		{ID: "gemini-1.5-pro", Name: "Gemini 1.5 Pro"},
		{ID: "gemini-1.5-flash", Name: "Gemini 1.5 Flash"},
	}
}
