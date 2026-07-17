// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"fmt"
	"net/http"
	"strings"
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
	defaultVLLMBaseURL      = "http://localhost:8000"
	defaultOpenAIBaseURL    = "https://api.openai.com/v1"
	defaultGeminiBaseURL    = "https://generativelanguage.googleapis.com/v1beta"
)

// Local default models are Gemma-class per ADR-0012/A23: the unbound
// local path must land on a non-Chinese model (Gemma default, Mistral
// as the EU alternative an operator picks explicitly in config).
const (
	defaultOllamaModel = "gemma3"
	defaultVLLMModel   = "google/gemma-3-12b-it"
)

// jsonSchemaFormatType is the structured-output format discriminator both the
// vLLM (OpenAI-compatible) and Anthropic adapters send for a schema-constrained
// completion.
const jsonSchemaFormatType = "json_schema"

// requestTimeout bounds a single model call. Generous because premium
// completions on long context are legitimately slow; per-call contexts
// tighten it where a caller has a real deadline.
const requestTimeout = 120 * time.Second

// Provider names — the vocabulary shared by the SelectBrain switch,
// knownProviders, and the sovereign-eligible localProviders set. One spelling
// each so a typo can't silently split "the switch accepts it" from "the config
// enum offers it".
const (
	providerFake             = "fake"
	providerAnthropic        = "anthropic"
	providerOllama           = "ollama"
	providerVLLM             = "vllm"
	providerOpenAICompatible = "openai_compatible"
	providerOpenAI           = "openai"
	providerGemini           = "gemini"
)

// knownProviders is the single source of truth for the provider names
// SelectBrain accepts — read by the default error below and by the config
// JSON-schema drift test. Add a provider here when you add its case.
var knownProviders = []string{providerFake, providerAnthropic, providerOllama, providerVLLM, providerOpenAICompatible, providerOpenAI, providerGemini}

// SelectBrain turns one binding into a Client (interfaces.md §4):
// "offline fake ↔ API key ↔ local, one line" — swapping providers is a
// config change, never a code change.
func SelectBrain(cfg ProviderConfig) (model.Client, error) {
	switch cfg.Provider {
	case providerFake:
		return NewFakeClient(), nil
	case providerAnthropic:
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
	case providerOllama:
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = defaultOllamaBaseURL
		}
		defaultModel := cfg.Model
		if defaultModel == "" {
			defaultModel = defaultOllamaModel
		}
		return &ollamaClient{
			http:         &http.Client{Timeout: requestTimeout},
			baseURL:      baseURL,
			defaultModel: defaultModel,
		}, nil
	case providerVLLM:
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = defaultVLLMBaseURL
		}
		defaultModel := cfg.Model
		if defaultModel == "" {
			defaultModel = defaultVLLMModel
		}
		return &openAICompatClient{
			http:         &http.Client{Timeout: requestTimeout},
			baseURL:      baseURL,
			apiKey:       "", // local vLLM: no auth
			localOnly:    true,
			defaultModel: defaultModel,
		}, nil
	case providerOpenAICompatible:
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("ai: provider openai_compatible needs an api key (BYOK — we provide no inference)")
		}
		if cfg.BaseURL == "" {
			return nil, fmt.Errorf("ai: provider openai_compatible needs a base_url (the vendor endpoint, e.g. https://api.openai.com/v1)")
		}
		return &openAICompatClient{
			http:         &http.Client{Timeout: requestTimeout},
			baseURL:      cfg.BaseURL,
			apiKey:       cfg.APIKey,
			localOnly:    false,
			defaultModel: cfg.Model,
		}, nil
	case providerOpenAI:
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("ai: provider openai needs an api key (BYOK — we provide no inference)")
		}
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = defaultOpenAIBaseURL
		}
		return &openaiClient{http: &http.Client{Timeout: requestTimeout}, baseURL: baseURL, apiKey: cfg.APIKey, defaultModel: cfg.Model}, nil
	case providerGemini:
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("ai: provider gemini needs an api key (BYOK — we provide no inference)")
		}
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = defaultGeminiBaseURL
		}
		return &geminiClient{http: &http.Client{Timeout: requestTimeout}, baseURL: baseURL, apiKey: cfg.APIKey, defaultModel: cfg.Model}, nil
	case "":
		return nil, fmt.Errorf("ai: binding has no provider")
	default:
		return nil, fmt.Errorf("ai: unknown provider %q (have: %s)", cfg.Provider, strings.Join(knownProviders, ", "))
	}
}
