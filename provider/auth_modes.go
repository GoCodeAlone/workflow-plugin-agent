package provider

// AuthModeInfo describes an authentication/deployment mode for a provider backend.
type AuthModeInfo struct {
	Mode        string // e.g. "personal", "direct", "bedrock"
	DisplayName string // e.g. "GitHub Copilot (Personal/IDE)"
	Description string // What this mode does
	Warning     string // ToS/usage concerns (empty if none)
	DocsURL     string // Link to official documentation
	ServerSafe  bool   // Whether this mode is appropriate for server/service use
}

// AllAuthModes returns metadata for all known provider authentication modes,
// including both implemented and scaffolded providers.
func AllAuthModes() []AuthModeInfo {
	return []AuthModeInfo{
		// Anthropic
		{Mode: "direct", DisplayName: "Anthropic (Direct API)", Description: "Direct access to Anthropic's Claude models via API key.", DocsURL: "https://platform.claude.com/docs/en/api/getting-started", ServerSafe: true},
		{Mode: "bedrock", DisplayName: "Anthropic (Amazon Bedrock)", Description: "Access Claude models via Amazon Bedrock using AWS IAM SigV4 authentication.", DocsURL: "https://platform.claude.com/docs/en/build-with-claude/claude-on-amazon-bedrock", ServerSafe: true},
		{Mode: "vertex", DisplayName: "Anthropic (Google Vertex AI)", Description: "Access Claude models via Google Cloud Vertex AI using Application Default Credentials.", DocsURL: "https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai", ServerSafe: true},
		{Mode: "foundry", DisplayName: "Anthropic (Azure AI Foundry)", Description: "Access Claude models via Microsoft Azure AI Foundry.", DocsURL: "https://platform.claude.com/docs/en/build-with-claude/claude-in-microsoft-foundry", ServerSafe: true},
		// OpenAI
		{Mode: "direct", DisplayName: "OpenAI (Direct API)", Description: "Direct access to OpenAI models via API key.", DocsURL: "https://platform.openai.com/docs/api-reference/introduction", ServerSafe: true},
		{Mode: "azure", DisplayName: "OpenAI (Azure OpenAI Service)", Description: "Access OpenAI models via Azure OpenAI Service.", DocsURL: "https://learn.microsoft.com/en-us/azure/ai-services/openai/reference", ServerSafe: true},
		{Mode: "openrouter", DisplayName: "OpenRouter", Description: "Access multiple AI models via OpenRouter's unified API.", DocsURL: "https://openrouter.ai/docs/api/reference/authentication", ServerSafe: true},
		// GitHub Copilot
		{Mode: "personal", DisplayName: "GitHub Copilot (Personal/IDE)", Description: "Uses GitHub Copilot's chat API via OAuth token exchange. For IDE/CLI use only.", Warning: "This mode uses Copilot's internal API. Server use may violate ToS.", DocsURL: "https://docs.github.com/en/copilot", ServerSafe: false},
		{Mode: "github_models", DisplayName: "GitHub Models", Description: "Access AI models via GitHub's Models marketplace using a fine-grained PAT.", DocsURL: "https://docs.github.com/en/rest/models/inference", ServerSafe: true},
		// Cohere
		{Mode: "direct", DisplayName: "Cohere (Direct API)", Description: "Direct access to Cohere's Command models via API key.", DocsURL: "https://docs.cohere.com/reference/chat", ServerSafe: true},
	}
}
