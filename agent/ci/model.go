package ci

import (
	"os"
	"strings"

	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/orchestrator"
)

func BuildModelOrchestratorFromEnvironment() (*orchestrator.Orchestrator, bool, error) {
	provider := strings.TrimSpace(os.Getenv("PROVIDER"))
	if provider == "" {
		provider = detectedModelProvider()
	}
	if provider == "" {
		return nil, false, nil
	}
	cfg := &config.Config{
		Provider: provider, ProviderAPIKey: os.Getenv("PROVIDER_API_KEY"), ProviderBaseURL: os.Getenv("PROVIDER_BASE_URL"),
		ReviewModel: os.Getenv("REVIEW_MODEL"), SmallModel: os.Getenv("SMALL_MODEL"), OrchestratorConfigPath: os.Getenv("ORCHESTRATOR_CONFIG"),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"), OpenAIAPIKey: os.Getenv("OPENAI_API_KEY"), OpenRouterAPIKey: os.Getenv("OPENROUTER_API_KEY"),
		OpenRouterBaseURL: firstNonEmptyValue(os.Getenv("OPENROUTER_BASE_URL"), "https://openrouter.ai/api"),
		DeepSeekAPIKey:    os.Getenv("DEEPSEEK_API_KEY"), DeepSeekBaseURL: firstNonEmptyValue(os.Getenv("DEEPSEEK_BASE_URL"), "https://api.deepseek.com"),
		MistralAPIKey: os.Getenv("MISTRAL_API_KEY"), GeminiAPIKey: os.Getenv("GEMINI_API_KEY"), OllamaBaseURL: os.Getenv("OLLAMA_BASE_URL"),
	}
	if cfg.ReviewModel == "" {
		cfg.ReviewModel = defaultCIModel(provider, true)
	}
	if cfg.SmallModel == "" {
		cfg.SmallModel = defaultCIModel(provider, false)
	}
	modelOrchestrator, err := orchestrator.BuildOrchestrator(cfg)
	return modelOrchestrator, err == nil, err
}

func detectedModelProvider() string {
	for _, item := range []struct{ provider, key string }{{"openrouter", "OPENROUTER_API_KEY"}, {"openai", "OPENAI_API_KEY"}, {"anthropic", "ANTHROPIC_API_KEY"}, {"deepseek", "DEEPSEEK_API_KEY"}, {"mistral", "MISTRAL_API_KEY"}, {"gemini", "GEMINI_API_KEY"}} {
		if strings.TrimSpace(os.Getenv(item.key)) != "" {
			return item.provider
		}
	}
	if os.Getenv("OLLAMA_BASE_URL") != "" {
		return "ollama"
	}
	if os.Getenv("PROVIDER_API_KEY") != "" {
		return "openai_compat"
	}
	return ""
}

func defaultCIModel(provider string, review bool) string {
	switch provider {
	case "openrouter":
		return "openrouter/free"
	case "openai":
		if review {
			return "gpt-4o"
		}
		return "gpt-4o-mini"
	case "anthropic":
		if review {
			return "claude-sonnet-4-6"
		}
		return "claude-haiku-4-5"
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
		return "model"
	}
}

func firstNonEmptyValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
