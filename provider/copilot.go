package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

const (
	defaultCopilotBaseURL   = "https://api.githubcopilot.com"
	defaultCopilotModel     = "gpt-4o"
	defaultCopilotMaxTokens = 4096
	copilotTokenExchangeURL = "https://api.github.com/copilot_internal/v2/token"
	copilotEditorVersion    = "ratchet/0.1.0"
)

// CopilotConfig holds configuration for the GitHub Copilot provider.
type CopilotConfig struct {
	Token      string
	Model      string
	BaseURL    string
	MaxTokens  int
	HTTPClient *http.Client
}

// CopilotProvider implements Provider using the GitHub Copilot Chat API.
// The API follows the OpenAI Chat Completions format.
type CopilotProvider struct {
	config      CopilotConfig
	mu          sync.Mutex
	bearerToken string
	expiresAt   time.Time
}

// copilotTokenResponse is the response from the Copilot token exchange endpoint.
type copilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

// NewCopilotProvider creates a new Copilot provider with the given config.
func NewCopilotProvider(cfg CopilotConfig) *CopilotProvider {
	if cfg.Model == "" {
		cfg.Model = defaultCopilotModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultCopilotBaseURL
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultCopilotMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &CopilotProvider{config: cfg}
}

func (p *CopilotProvider) Name() string { return "copilot" }

func (p *CopilotProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "personal",
		DisplayName: "GitHub Copilot (Personal/IDE)",
		Description: "Uses GitHub Copilot's chat completions API via OAuth token exchange. Intended for IDE/CLI use under a Copilot Individual or Business subscription.",
		Warning:     "This mode uses Copilot's internal API intended for IDE integrations. Using it in server/service contexts may violate GitHub Copilot Terms of Service (https://docs.github.com/en/site-policy/github-terms/github-terms-for-additional-products-and-features).",
		DocsURL:     "https://docs.github.com/en/copilot",
		ServerSafe:  false,
	}
}

func (p *CopilotProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	if err := p.ensureBearerToken(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	token := p.bearerToken
	p.mu.Unlock()

	client := p.newSDKClient(token)
	params := openaisdk.ChatCompletionNewParams{
		Model:     shared.ChatModel(p.config.Model),
		Messages:  toOpenAIMessages(messages),
		MaxTokens: openaisdk.Int(int64(p.config.MaxTokens)),
	}
	if len(tools) > 0 {
		params.Tools = toOpenAITools(tools)
	}
	resp, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("copilot: %w", err)
	}
	return fromOpenAIResponse(resp)
}

func (p *CopilotProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	if err := p.ensureBearerToken(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	token := p.bearerToken
	p.mu.Unlock()

	client := p.newSDKClient(token)
	params := openaisdk.ChatCompletionNewParams{
		Model:     shared.ChatModel(p.config.Model),
		Messages:  toOpenAIMessages(messages),
		MaxTokens: openaisdk.Int(int64(p.config.MaxTokens)),
	}
	if len(tools) > 0 {
		params.Tools = toOpenAITools(tools)
	}
	stream := client.Chat.Completions.NewStreaming(ctx, params)
	ch := make(chan StreamEvent, 16)
	go streamOpenAIEvents(stream, ch)
	return ch, nil
}

// newSDKClient creates a per-request OpenAI SDK client with Copilot auth headers.
func (p *CopilotProvider) newSDKClient(bearerToken string) openaisdk.Client {
	return openaisdk.NewClient(
		option.WithAPIKey(bearerToken),
		option.WithBaseURL(p.config.BaseURL),
		option.WithHTTPClient(p.config.HTTPClient),
		option.WithHeader("Copilot-Integration-Id", "vscode-chat"),
		option.WithHeader("Editor-Version", "vscode/1.100.0"),
		option.WithHeader("Editor-Plugin-Version", copilotEditorVersion),
	)
}

// ensureBearerToken exchanges the GitHub OAuth token for a short-lived Copilot
// bearer token, caching it until 60 seconds before expiry.
func (p *CopilotProvider) ensureBearerToken(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.bearerToken != "" && time.Now().Before(p.expiresAt.Add(-60*time.Second)) {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotTokenExchangeURL, nil)
	if err != nil {
		return fmt.Errorf("copilot: create token exchange request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+p.config.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("copilot: token exchange request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("copilot: read token exchange response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("copilot: token exchange failed (status %d): %s", resp.StatusCode, truncate(string(body), 200))
	}

	var tokenResp copilotTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return fmt.Errorf("copilot: parse token exchange response: %w", err)
	}

	p.bearerToken = tokenResp.Token
	p.expiresAt = time.Unix(tokenResp.ExpiresAt, 0)
	return nil
}
