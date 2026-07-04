package ai

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// ProviderConfig is one tier→provider binding from ai-routing.yaml —
// the only place vendor names appear.
type ProviderConfig struct {
	Provider string `yaml:"provider"` // "fake" | "anthropic" | "ollama"
	Model    string `yaml:"model"`    // provider-native model id, resolved from the logical tier
	BaseURL  string `yaml:"base_url"` // endpoint override; empty means the provider default
	APIKey   string `yaml:"api_key"`  // BYOK credential for cloud providers (ADR-0020)
}

// Provider defaults. The Anthropic URL is the vendor's public API; a
// hosting-partner or proxy deployment overrides BaseURL in config.
const (
	defaultAnthropicBaseURL = "https://api.anthropic.com"
	defaultOllamaBaseURL    = "http://localhost:11434"
)

// requestTimeout bounds a single model call. Generous because premium
// completions on long context are legitimately slow; per-call contexts
// tighten it where a caller has a real deadline.
const requestTimeout = 120 * time.Second

// SelectBrain turns one binding into a Client (interfaces.md §4):
// "offline fake ↔ API key ↔ local, one line" — swapping providers is a
// config change, never a code change.
func SelectBrain(cfg ProviderConfig) (model.Client, error) {
	switch cfg.Provider {
	case "fake":
		return NewFakeClient(), nil
	case "anthropic":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("ai: provider anthropic needs an api key (BYOK — we provide no inference)")
		}
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = defaultAnthropicBaseURL
		}
		return &anthropicClient{
			http:         &http.Client{Timeout: requestTimeout},
			baseURL:      baseURL,
			apiKey:       cfg.APIKey,
			defaultModel: cfg.Model,
		}, nil
	case "ollama":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = defaultOllamaBaseURL
		}
		return &ollamaClient{
			http:         &http.Client{Timeout: requestTimeout},
			baseURL:      baseURL,
			defaultModel: cfg.Model,
		}, nil
	case "":
		return nil, fmt.Errorf("ai: binding has no provider")
	default:
		return nil, fmt.Errorf("ai: unknown provider %q (have: fake, anthropic, ollama)", cfg.Provider)
	}
}
