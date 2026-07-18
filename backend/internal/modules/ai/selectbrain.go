// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// ProviderConfig is one tier→provider binding from ai-routing.yaml — the only
// place vendor names appear. It carries NO secret: a cloud provider's BYOK key
// is read at boot from the provider's conventional environment variable (see
// cloudKeyEnv), so secrets live in the environment, never a config file. The
// parser rejects unknown keys, so a stray `api_key:` here is a loud error that
// points the operator at the env var (ADR-0020: BYOK, we provide no inference).
type ProviderConfig struct {
	Provider string `yaml:"provider"` // one of knownProviders
	Model    string `yaml:"model"`    // provider-native model id, resolved from the logical tier
	BaseURL  string `yaml:"base_url"` // endpoint override; empty means the provider default
}

// Provider defaults. The Anthropic URL is the vendor's public API; a
// hosting-partner or proxy deployment overrides BaseURL in config.
const (
	defaultAnthropicBaseURL = "https://api.anthropic.com"
	defaultOllamaBaseURL    = "http://localhost:11434"
	defaultVLLMBaseURL      = "http://localhost:8000"
	// defaultOpenAIBaseURL is the host root WITHOUT a version segment: the
	// OpenAI-wire transport appends "/v1/responses" / "/v1/embeddings", so a base
	// that already carried "/v1" would double it (…/v1/v1/responses → 404). Same
	// version-less convention as Anthropic and vLLM.
	defaultOpenAIBaseURL = "https://api.openai.com"
	// defaultGeminiBaseURL keeps the /v1beta version segment: Gemini paths are
	// version-relative (":generateContent" under "/models/…"), the mirror of the
	// OpenAI-wire convention above.
	defaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"
)

// Local default models are Gemma-class per ADR-0012/A23: the unbound
// local path must land on a non-Chinese model (Gemma default, Mistral
// as the EU alternative an operator picks explicitly in config).
const (
	defaultOllamaModel = "gemma3"
	defaultVLLMModel   = "google/gemma-3-12b-it"
)

// jsonSchemaFormatType is the structured-output format discriminator the
// adapters send for a schema-constrained completion (the OpenAI-wire
// response_format, Anthropic's output_config.format, and the OpenAI Responses
// text.format all share this "json_schema" value).
const jsonSchemaFormatType = "json_schema"

// requestTimeout bounds a single model call. Generous because premium
// completions on long context are legitimately slow — a streamed corpus
// extraction emits ten-thousand-token answers over minutes; per-call
// contexts tighten it where a caller has a real deadline.
const requestTimeout = 300 * time.Second

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
		key := cloudKey(providerAnthropic)
		if key == "" {
			return nil, byokKeyRequired(providerAnthropic)
		}
		return &anthropicClient{
			http:         &http.Client{Timeout: requestTimeout},
			baseURL:      defaulted(cfg.BaseURL, defaultAnthropicBaseURL),
			apiKey:       key,
			defaultModel: cfg.Model,
		}, nil
	case providerOllama:
		return &ollamaClient{
			http:         &http.Client{Timeout: requestTimeout},
			baseURL:      defaulted(cfg.BaseURL, defaultOllamaBaseURL),
			defaultModel: defaulted(cfg.Model, defaultOllamaModel),
		}, nil
	case providerVLLM:
		return &openAICompatClient{
			http:         &http.Client{Timeout: requestTimeout},
			baseURL:      defaulted(cfg.BaseURL, defaultVLLMBaseURL),
			apiKey:       "", // local vLLM: no auth
			localOnly:    true,
			defaultModel: defaulted(cfg.Model, defaultVLLMModel),
		}, nil
	case providerOpenAICompatible:
		key := cloudKey(providerOpenAICompatible)
		if key == "" {
			return nil, byokKeyRequired(providerOpenAICompatible)
		}
		if cfg.BaseURL == "" {
			return nil, fmt.Errorf("ai: provider openai_compatible needs a base_url (the vendor host root — no version segment, the adapter adds /v1; e.g. https://api.mistral.ai)")
		}
		return &openAICompatClient{
			http:         &http.Client{Timeout: requestTimeout},
			baseURL:      cfg.BaseURL,
			apiKey:       key,
			localOnly:    false,
			defaultModel: cfg.Model,
		}, nil
	case providerOpenAI:
		key := cloudKey(providerOpenAI)
		if key == "" {
			return nil, byokKeyRequired(providerOpenAI)
		}
		return &openaiClient{
			http:         &http.Client{Timeout: requestTimeout},
			baseURL:      defaulted(cfg.BaseURL, defaultOpenAIBaseURL),
			apiKey:       key,
			defaultModel: cfg.Model,
		}, nil
	case providerGemini:
		key := cloudKey(providerGemini)
		if key == "" {
			return nil, byokKeyRequired(providerGemini)
		}
		return &geminiClient{
			http:         &http.Client{Timeout: requestTimeout},
			baseURL:      defaulted(cfg.BaseURL, defaultGeminiBaseURL),
			apiKey:       key,
			defaultModel: cfg.Model,
		}, nil
	case "":
		return nil, fmt.Errorf("ai: binding has no provider")
	default:
		return nil, fmt.Errorf("ai: unknown provider %q (have: %s)", cfg.Provider, strings.Join(knownProviders, ", "))
	}
}

// defaulted returns val, or fallback when val is empty — the base-url / model
// defaulting every provider case shares.
func defaulted(val, fallback string) string {
	if val == "" {
		return fallback
	}
	return val
}

// cloudKeyEnv maps a cloud provider to the environment variable its BYOK key is
// read from. Secrets live in the environment; the routing file names only the
// provider (12-factor). The names match the vendor SDK conventions so an
// operator who already exports OPENAI_API_KEY / GEMINI_API_KEY needs no extra
// wiring. openai_compatible has no vendor convention, so it gets a namespaced one.
var cloudKeyEnv = map[string]string{
	providerAnthropic:        "ANTHROPIC_API_KEY",
	providerOpenAI:           "OPENAI_API_KEY",
	providerGemini:           "GEMINI_API_KEY",
	providerOpenAICompatible: "OPENAI_COMPATIBLE_API_KEY",
}

// cloudKey returns the BYOK key for a cloud provider from its conventional
// environment variable, or "" when unset (the caller fails closed).
func cloudKey(provider string) string {
	if env := cloudKeyEnv[provider]; env != "" {
		return os.Getenv(env)
	}
	return ""
}

// byokKeyRequired is the fail-closed error for a cloud provider bound without a
// key in the environment: Margince provides no inference, so the customer's key
// is mandatory (ADR-0020). It names the env var to set so the fix is obvious.
func byokKeyRequired(provider string) error {
	if env := cloudKeyEnv[provider]; env != "" {
		return fmt.Errorf("ai: provider %s needs an api key — set %s in the environment (BYOK — we provide no inference)", provider, env)
	}
	return fmt.Errorf("ai: provider %s needs an api key (BYOK — we provide no inference)", provider)
}
