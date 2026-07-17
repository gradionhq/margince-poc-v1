// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"strings"
	"testing"
)

func TestParseRoutingValidatesAtStartup(t *testing.T) {
	valid := `
tiers:
  local_small: {provider: fake}
  cheap_cloud: {provider: anthropic, model: claude-haiku, api_key: k}
embeddings: {provider: fake}
profile: eu_hosted
`
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"valid", valid, ""},
		{"missing profile", strings.Replace(valid, "profile: eu_hosted", "", 1), "profile is required"},
		{"unknown profile", strings.Replace(valid, "eu_hosted", "hybrid", 1), "unknown profile"},
		{"unknown tier", strings.Replace(valid, "local_small", "medium_cloud", 1), "unknown tier"},
		{"tier without provider", strings.Replace(valid, "{provider: fake}\n  cheap_cloud", "{model: gemma}\n  cheap_cloud", 1), "no provider"},
		{"no embeddings lane", strings.Replace(valid, "embeddings: {provider: fake}", "", 1), "embeddings lane has no provider"},
		{"typo'd key rejected", strings.Replace(valid, "tiers:", "tierz:", 1), "field tierz not found"},
		{"sovereign refuses cloud chat tier", strings.Replace(valid, "profile: eu_hosted", "profile: sovereign", 1), "sovereign forbids cloud provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRouting([]byte(tc.yaml))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("valid config rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// A cloud provider on any tier or the embeddings lane is refused under the
// sovereign profile — zero egress by construction (spec §3.6).
func TestSovereignRefusesOpenAICompatible(t *testing.T) {
	cfg := []byte(`
profile: sovereign
tiers:
  cheap_cloud: {provider: openai_compatible, base_url: https://api.mistral.ai/v1, api_key: k, model: m}
embeddings: {provider: ollama, model: bge-m3}
`)
	if _, err := ParseRouting(cfg); err == nil || !strings.Contains(err.Error(), "sovereign forbids cloud provider") {
		t.Fatalf("want sovereign-forbids-cloud, got %v", err)
	}
}

// LocalOnly (the runtime capability) and localProviders (the parse-time set)
// are two encodings of "is this cloud"; they may never disagree.
func TestLocalOnlyMatchesLocalProvidersForEveryProvider(t *testing.T) {
	built := map[string]ProviderConfig{
		"fake":              {Provider: "fake"},
		"anthropic":         {Provider: "anthropic", APIKey: "k", Model: "m"},
		"ollama":            {Provider: "ollama", Model: "m"},
		"vllm":              {Provider: "vllm", Model: "m"},
		"openai_compatible": {Provider: "openai_compatible", APIKey: "k", BaseURL: "https://x", Model: "m"},
		"openai":            {Provider: "openai", APIKey: "k", Model: "m"},
		"gemini":            {Provider: "gemini", APIKey: "k", Model: "m"},
	}
	for _, name := range knownProviders {
		cfg, ok := built[name]
		if !ok {
			t.Fatalf("knownProviders has %q with no build recipe in this test — add one", name)
		}
		client, err := SelectBrain(cfg)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, want := client.Caps().LocalOnly, localProviders[name]; got != want {
			t.Fatalf("%s: Caps().LocalOnly=%v but localProviders=%v — encodings disagree", name, got, want)
		}
	}
}

func TestParseRoutingSovereignAllLocalIsValid(t *testing.T) {
	cfg, err := ParseRouting([]byte(`
tiers:
  local_small: {provider: ollama, model: gemma3}
  local_large: {provider: ollama, model: llama3.3:70b}
embeddings: {provider: ollama, model: bge-m3}
profile: sovereign
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != ProfileSovereign || len(cfg.Tiers) != 2 {
		t.Fatalf("unexpected parse: %+v", cfg)
	}
}
