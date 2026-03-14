package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/vertex"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	defaultVertexModel  = "claude-sonnet-4@20250514"
	defaultVertexRegion = "us-east5"
)

// AnthropicVertexConfig configures the Anthropic provider for Google Vertex AI.
// Uses GCP Application Default Credentials (ADC) or explicit OAuth2 tokens.
type AnthropicVertexConfig struct {
	// ProjectID is the GCP project ID.
	ProjectID string
	// Region is the GCP region (e.g. "us-east5", "europe-west1").
	Region string
	// Model is the Vertex model ID (e.g. "claude-sonnet-4@20250514").
	Model string
	// MaxTokens limits the response length.
	MaxTokens int
	// CredentialsJSON is the GCP service account JSON (optional if using ADC).
	CredentialsJSON string
	// TokenSource provides OAuth2 tokens (optional, for testing or custom auth).
	// If set, CredentialsJSON and ADC are ignored.
	TokenSource oauth2.TokenSource
	// HTTPClient is the HTTP client to use (defaults to http.DefaultClient).
	HTTPClient *http.Client
}

// anthropicVertexProvider accesses Anthropic models via Google Vertex AI.
type anthropicVertexProvider struct {
	client anthropic.Client
	config AnthropicVertexConfig
}

// NewAnthropicVertexProvider creates a provider that accesses Claude via Google Vertex AI.
//
// Docs: https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai
func NewAnthropicVertexProvider(cfg AnthropicVertexConfig) (*anthropicVertexProvider, error) {
	if cfg.ProjectID == "" {
		return nil, fmt.Errorf("anthropic_vertex: project ID is required")
	}
	if cfg.Region == "" {
		cfg.Region = defaultVertexRegion
	}
	if cfg.Model == "" {
		cfg.Model = defaultVertexModel
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultAnthropicMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	var client anthropic.Client

	baseURL := fmt.Sprintf("https://%s-aiplatform.googleapis.com/", cfg.Region)

	if cfg.TokenSource != nil {
		// Testing / custom-auth path: use middleware for token injection and path rewriting.
		client = anthropic.NewClient(
			option.WithBaseURL(baseURL),
			option.WithHTTPClient(cfg.HTTPClient),
			option.WithMiddleware(vertexPathRewriteMiddleware(cfg.Region, cfg.ProjectID)),
			option.WithMiddleware(vertexBearerTokenMiddleware(cfg.TokenSource)),
		)
	} else if cfg.CredentialsJSON != "" {
		creds, err := google.CredentialsFromJSON(context.Background(), []byte(cfg.CredentialsJSON),
			"https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("anthropic_vertex: parse credentials JSON: %w", err)
		}
		client = anthropic.NewClient(
			vertex.WithCredentials(context.Background(), cfg.Region, cfg.ProjectID, creds),
		)
	} else {
		client = anthropic.NewClient(
			vertex.WithGoogleAuth(context.Background(), cfg.Region, cfg.ProjectID,
				"https://www.googleapis.com/auth/cloud-platform"),
		)
	}

	return &anthropicVertexProvider{client: client, config: cfg}, nil
}

func (p *anthropicVertexProvider) Name() string { return "anthropic_vertex" }

func (p *anthropicVertexProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "vertex",
		DisplayName: "Anthropic (Google Vertex AI)",
		Description: "Access Claude models via Google Cloud Vertex AI using Application Default Credentials (ADC) or service account JSON.",
		DocsURL:     "https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai",
		ServerSafe:  true,
	}
}

func (p *anthropicVertexProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	params := toAnthropicParams(p.config.Model, p.config.MaxTokens, messages, tools)
	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic_vertex: %w", err)
	}
	return fromAnthropicMessage(msg)
}

func (p *anthropicVertexProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	params := toAnthropicParams(p.config.Model, p.config.MaxTokens, messages, tools)
	stream := p.client.Messages.NewStreaming(ctx, params)
	if stream.Err() != nil {
		return nil, fmt.Errorf("anthropic_vertex: %w", stream.Err())
	}
	ch := make(chan StreamEvent, 16)
	go streamAnthropicEvents(stream, ch)
	return ch, nil
}

// vertexPathRewriteMiddleware rewrites /v1/messages to the Vertex AI model path.
func vertexPathRewriteMiddleware(region, projectID string) option.Middleware {
	return func(r *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		if r.Body == nil || r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
			return next(r)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body.Close()

		var params struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(body, &params)

		// Remove model and stream from body (Vertex uses URL path instead)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		delete(m, "model")
		delete(m, "stream")
		body, _ = json.Marshal(m)

		specifier := "rawPredict"
		if params.Stream {
			specifier = "streamRawPredict"
		}
		r.URL.Path = fmt.Sprintf("/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s",
			projectID, region, params.Model, specifier)

		reader := bytes.NewReader(body)
		r.Body = io.NopCloser(reader)
		r.GetBody = func() (io.ReadCloser, error) {
			_, _ = reader.Seek(0, 0)
			return io.NopCloser(reader), nil
		}
		r.ContentLength = int64(len(body))

		return next(r)
	}
}

// vertexBearerTokenMiddleware adds a GCP Bearer token to each request.
func vertexBearerTokenMiddleware(ts oauth2.TokenSource) option.Middleware {
	return func(r *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		token, err := ts.Token()
		if err != nil {
			return nil, fmt.Errorf("anthropic_vertex: get token: %w", err)
		}
		r.Header.Set("Authorization", "Bearer "+token.AccessToken)
		r.Header.Set("anthropic-version", anthropicAPIVersion)
		return next(r)
	}
}
