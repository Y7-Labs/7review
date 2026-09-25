package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime settings, loaded from environment variables.
type Config struct {
	// ── Pipeline ──────────────────────────────────────────────────────────
	SkillsDir                   string
	InputProfilePath            string
	CorpusRoot                  string
	MemoryDir                   string
	InstructionsPath            string
	MaxDiffTokens               int
	MaxSupportingCorpusSections int
	HILChannel                  string
	ListenAddr                  string
	ReadHeaderTimeout           int
	ReadTimeout                 int
	WriteTimeout                int
	IdleTimeout                 int
	APIToken                    string
	WebhookWorkers              int
	WebhookQueueSize            int
	WebhookJobTimeout           int
	WebhookReviewMode           string
	ReviewLabelInclude          []string
	ReviewLabelExclude          []string
	ReviewAllowedProjects       []string
	ReviewAllowedRepos          []string
	ReviewBranchInclude         []string
	ReviewBranchExclude         []string
	HeadroomURL                 string
	HeadroomTimeout             int
	MemPalaceURL                string
	MemPalaceTimeout            int
	ChannelInboundToken         string
	ChannelAuthorizedSenders    []string
	PolicyV2Mode                string
	PolicyV2Path                string
	PolicyRuntimeCapabilities   []string

	// ── GitLab ────────────────────────────────────────────────────────────
	GitLabURL     string
	GitLabToken   string
	WebhookSecret string

	// ── GitHub ────────────────────────────────────────────────────────────
	GitHubAPIURL        string
	GitHubToken         string
	GitHubWebhookSecret string

	// ── Multi-LLM Orchestration ───────────────────────────────────────────
	// OrchestratorConfigPath points to a YAML file defining role→provider chains.
	// If empty, DefaultOrchestratorConfig is used with the env vars below.
	OrchestratorConfigPath string

	// Fallback single-provider mode (used when no orchestrator config file exists).
	// PROVIDER sets which provider handles all roles.
	// Supported: anthropic | openai | openrouter | deepseek | mistral | gemini | ollama | openai_compat
	Provider        string
	ProviderAPIKey  string
	ProviderBaseURL string // optional override (e.g. for openai_compat or Ollama)

	// ReviewModel and SmallModel are the model strings for the fallback single-provider mode.
	ReviewModel    string // used for RoleReasoner
	SmallModel     string // used for RoleFormatter
	EmbeddingModel string // used by memory/vector retrieval sidecars

	// Per-provider API keys — used when the orchestrator config references
	// multiple providers. All optional; only needed if that provider is used.
	AnthropicAPIKey   string
	OpenAIAPIKey      string
	MistralAPIKey     string
	GeminiAPIKey      string
	OllamaBaseURL     string
	OpenRouterAPIKey  string
	OpenRouterBaseURL string
	DeepSeekAPIKey    string
	DeepSeekBaseURL   string
}

// LoadConfig reads all required settings from the environment.
func LoadConfig() (*Config, error) {
	c := &Config{
		// Pipeline
		SkillsDir:                   getEnv("SKILLS_DIR", "./agent/skills"),
		InputProfilePath:            getEnv("INPUT_PROFILE", "./profiles/default.input-profile.json"),
		CorpusRoot:                  getEnv("CORPUS_ROOT", "."),
		MemoryDir:                   getEnv("MEMORY_DIR", "./.7review"),
		InstructionsPath:            getEnv("INSTRUCTIONS_PATH", "./agent/instructions.md"),
		MaxDiffTokens:               getEnvInt("MAX_DIFF_TOKENS", 6000),
		MaxSupportingCorpusSections: getEnvInt("MAX_SUPPORTING_CORPUS_SECTIONS", 3),
		HILChannel:                  getEnv("HIL_CHANNEL", "gitlab_note"),
		ListenAddr:                  getEnv("LISTEN_ADDR", ":8080"),
		ReadHeaderTimeout:           getEnvInt("HTTP_READ_HEADER_TIMEOUT_MS", 5000),
		ReadTimeout:                 getEnvInt("HTTP_READ_TIMEOUT_MS", 30000),
		WriteTimeout:                getEnvInt("HTTP_WRITE_TIMEOUT_MS", 120000),
		IdleTimeout:                 getEnvInt("HTTP_IDLE_TIMEOUT_MS", 120000),
		APIToken:                    os.Getenv("REVIEW_API_TOKEN"),
		WebhookWorkers:              getEnvInt("WEBHOOK_WORKERS", 4),
		WebhookQueueSize:            getEnvInt("WEBHOOK_QUEUE_SIZE", 128),
		WebhookJobTimeout:           getEnvInt("WEBHOOK_JOB_TIMEOUT_MS", 15*60*1000),
		WebhookReviewMode:           getEnv("WEBHOOK_REVIEW_MODE", "manual_first"),
		ReviewLabelInclude:          getEnvList("REVIEW_LABEL_INCLUDE", "7review,ready-for-review"),
		ReviewLabelExclude:          getEnvList("REVIEW_LABEL_EXCLUDE", "no-review,wip,draft"),
		ReviewAllowedProjects:       getEnvList("REVIEW_ALLOWED_PROJECTS", ""),
		ReviewAllowedRepos:          getEnvList("REVIEW_ALLOWED_REPOS", ""),
		ReviewBranchInclude:         getEnvList("REVIEW_BRANCH_INCLUDE", ""),
		ReviewBranchExclude:         getEnvList("REVIEW_BRANCH_EXCLUDE", ""),
		HeadroomURL:                 os.Getenv("HEADROOM_URL"),
		HeadroomTimeout:             getEnvInt("HEADROOM_TIMEOUT_MS", 30000),
		MemPalaceURL:                os.Getenv("MEMPALACE_URL"),
		MemPalaceTimeout:            getEnvInt("MEMPALACE_TIMEOUT_MS", 240000),
		ChannelInboundToken:         os.Getenv("CHANNEL_INBOUND_TOKEN"),
		ChannelAuthorizedSenders:    getEnvList("CHANNEL_AUTHORIZED_SENDERS", ""),
		PolicyV2Mode:                getEnv("POLICY_V2_MODE", "legacy"),
		PolicyV2Path:                getEnv("POLICY_V2_PATH", ".7review/review.yaml"),
		PolicyRuntimeCapabilities:   getEnvList("POLICY_RUNTIME_CAPABILITIES", "repo.read,model.review"),

		// GitLab
		GitLabURL:     os.Getenv("GITLAB_URL"),
		GitLabToken:   os.Getenv("GITLAB_TOKEN"),
		WebhookSecret: os.Getenv("GITLAB_WEBHOOK_SECRET"),

		// GitHub
		GitHubAPIURL:        getEnv("GITHUB_API_URL", "https://api.github.com"),
		GitHubToken:         os.Getenv("GITHUB_TOKEN"),
		GitHubWebhookSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"),

		// Orchestration
		OrchestratorConfigPath: os.Getenv("ORCHESTRATOR_CONFIG"),

		// Fallback single-provider mode
		Provider:        getEnv("PROVIDER", "anthropic"),
		ProviderAPIKey:  os.Getenv("PROVIDER_API_KEY"),
		ProviderBaseURL: os.Getenv("PROVIDER_BASE_URL"),
		ReviewModel:     os.Getenv("REVIEW_MODEL"),
		SmallModel:      os.Getenv("SMALL_MODEL"),
		EmbeddingModel:  os.Getenv("EMBEDDING_MODEL"),

		// Per-provider keys (multi-provider orchestration)
		AnthropicAPIKey:   os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:      os.Getenv("OPENAI_API_KEY"),
		MistralAPIKey:     os.Getenv("MISTRAL_API_KEY"),
		GeminiAPIKey:      os.Getenv("GEMINI_API_KEY"),
		OllamaBaseURL:     os.Getenv("OLLAMA_BASE_URL"),
		OpenRouterAPIKey:  os.Getenv("OPENROUTER_API_KEY"),
		OpenRouterBaseURL: getEnv("OPENROUTER_BASE_URL", defaultBaseURL("openrouter")),
		DeepSeekAPIKey:    os.Getenv("DEEPSEEK_API_KEY"),
		DeepSeekBaseURL:   getEnv("DEEPSEEK_BASE_URL", defaultBaseURL("deepseek")),
	}
	if c.ReviewModel == "" {
		c.ReviewModel = defaultModel(c.Provider, true)
	}
	if c.SmallModel == "" {
		c.SmallModel = defaultModel(c.Provider, false)
	}
	if c.EmbeddingModel == "" {
		c.EmbeddingModel = defaultEmbeddingModel(c.Provider, c.OllamaBaseURL)
	}

	var missing []string
	hasGitLab := c.GitLabURL != "" && c.GitLabToken != "" && c.WebhookSecret != ""
	hasGitHub := c.GitHubAPIURL != "" && c.GitHubToken != "" && c.GitHubWebhookSecret != ""
	if !hasGitLab && !hasGitHub {
		missing = append(missing, "GitLab or GitHub webhook/API credentials")
	}
	if c.HeadroomURL == "" {
		missing = append(missing, "HEADROOM_URL")
	}
	if c.MemPalaceURL == "" {
		missing = append(missing, "MEMPALACE_URL")
	}
	if c.APIToken == "" {
		missing = append(missing, "REVIEW_API_TOKEN")
	}
	for _, item := range []struct {
		key   string
		value int
	}{
		{"MAX_DIFF_TOKENS", c.MaxDiffTokens},
		{"MAX_SUPPORTING_CORPUS_SECTIONS", c.MaxSupportingCorpusSections},
		{"HTTP_READ_HEADER_TIMEOUT_MS", c.ReadHeaderTimeout},
		{"HTTP_READ_TIMEOUT_MS", c.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT_MS", c.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT_MS", c.IdleTimeout},
		{"WEBHOOK_WORKERS", c.WebhookWorkers},
		{"WEBHOOK_QUEUE_SIZE", c.WebhookQueueSize},
		{"WEBHOOK_JOB_TIMEOUT_MS", c.WebhookJobTimeout},
		{"HEADROOM_TIMEOUT_MS", c.HeadroomTimeout},
		{"MEMPALACE_TIMEOUT_MS", c.MemPalaceTimeout},
	} {
		if err := validatePositiveEnvInt(item.key, item.value); err != nil {
			missing = append(missing, err.Error())
		}
	}
	switch c.WebhookReviewMode {
	case "manual_first", "auto", "off":
	default:
		missing = append(missing, "WEBHOOK_REVIEW_MODE must be one of: manual_first, auto, off")
	}
	switch c.PolicyV2Mode {
	case "legacy", "preview", "enforce":
	default:
		missing = append(missing, "POLICY_V2_MODE must be one of: legacy, preview, enforce")
	}
	if c.PolicyV2Mode != "legacy" && strings.TrimSpace(c.PolicyV2Path) == "" {
		missing = append(missing, "POLICY_V2_PATH is required when POLICY_V2_MODE is preview or enforce")
	}
	if c.PolicyV2Mode != "legacy" && !validRepositoryPolicyPath(c.PolicyV2Path) {
		missing = append(missing, "POLICY_V2_PATH must be a repository-relative .yaml, .yml, or .json path without .. segments")
	}

	// At least one LLM provider key must be present.
	hasProvider := c.AnthropicAPIKey != "" || c.OpenAIAPIKey != "" ||
		c.MistralAPIKey != "" || c.GeminiAPIKey != "" ||
		c.OllamaBaseURL != "" || c.OpenRouterAPIKey != "" ||
		c.DeepSeekAPIKey != "" || c.ProviderAPIKey != "" ||
		(c.Provider == "openai_compat" && c.ProviderBaseURL != "")
	if !hasProvider {
		missing = append(missing, "at least one of: ANTHROPIC_API_KEY, OPENAI_API_KEY, OPENROUTER_API_KEY, DEEPSEEK_API_KEY, MISTRAL_API_KEY, GEMINI_API_KEY, OLLAMA_BASE_URL, PROVIDER_API_KEY")
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %v", missing)
	}

	return c, nil
}

func defaultBaseURL(provider string) string {
	switch provider {
	case "openrouter":
		return "https://openrouter.ai/api"
	case "deepseek":
		return "https://api.deepseek.com"
	case "ollama":
		return "http://localhost:11434"
	default:
		return ""
	}
}

func defaultModel(provider string, review bool) string {
	switch provider {
	case "openai":
		if review {
			return "gpt-4o"
		}
		return "gpt-4o-mini"
	case "openrouter":
		if review {
			return "openai/gpt-4o"
		}
		return "openai/gpt-4o-mini"
	case "deepseek":
		return "deepseek-chat"
	case "mistral":
		if review {
			return "mistral-large-latest"
		}
		return "mistral-small-latest"
	case "gemini":
		if review {
			return "gemini-1.5-pro"
		}
		return "gemini-1.5-flash"
	case "ollama":
		if review {
			return "deepseek-coder-v2:16b"
		}
		return "qwen2.5-coder-7b-16k:latest"
	default:
		if review {
			return "claude-sonnet-4-6"
		}
		return "claude-haiku-4-5-20251001"
	}
}

func defaultEmbeddingModel(provider string, ollamaBaseURL string) string {
	if provider == "ollama" || ollamaBaseURL != "" {
		return "nomic-embed-text:latest"
	}
	return ""
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func validRepositoryPolicyPath(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "" {
			return false
		}
	}
	lower := strings.ToLower(value)
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".json")
}

func getEnvList(key, fallback string) []string {
	raw := getEnv(key, fallback)
	if raw == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, part := range strings.Split(raw, ",") {
		item := strings.TrimSpace(part)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func validatePositiveEnvInt(key string, value int) error {
	raw, ok := os.LookupEnv(key)
	if ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("%s must be an integer", key)
		}
		value = parsed
	}
	if value <= 0 {
		return fmt.Errorf("%s must be greater than zero", key)
	}
	return nil
}
