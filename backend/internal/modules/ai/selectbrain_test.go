// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"strings"
	"testing"
)

// The B-EP06.2 acceptance shape: "offline fake ↔ API key ↔ local, one
// line" — each provider is one config value away.
func TestSelectBrainOneLinePerProvider(t *testing.T) {
	cases := []struct {
		name      string
		cfg       ProviderConfig
		wantErr   string
		localOnly bool
	}{
		{name: "offline fake", cfg: ProviderConfig{Provider: "fake"}, localOnly: true},
		{name: "cloud byok", cfg: ProviderConfig{Provider: "anthropic", APIKey: "k", Model: "claude-x"}, localOnly: false},
		{name: "local", cfg: ProviderConfig{Provider: "ollama", Model: "gemma3"}, localOnly: true},
		{name: "cloud without key fails closed", cfg: ProviderConfig{Provider: "anthropic"}, wantErr: "api key"},
		{name: "openai-compat cloud byok", cfg: ProviderConfig{Provider: "openai_compatible", APIKey: "k", BaseURL: "https://api.mistral.ai/v1", Model: "mistral-small-latest"}, localOnly: false},
		{name: "openai-compat without key fails closed", cfg: ProviderConfig{Provider: "openai_compatible", BaseURL: "https://x"}, wantErr: "api key"},
		{name: "openai-compat without base_url fails closed", cfg: ProviderConfig{Provider: "openai_compatible", APIKey: "k"}, wantErr: "base_url"},
		{name: "openai native byok", cfg: ProviderConfig{Provider: "openai", APIKey: "k", Model: "gpt-x"}, localOnly: false},
		{name: "openai without key fails closed", cfg: ProviderConfig{Provider: "openai"}, wantErr: "api key"},
		{name: "gemini native byok", cfg: ProviderConfig{Provider: "gemini", APIKey: "k", Model: "gemini-x"}, localOnly: false},
		{name: "gemini without key fails closed", cfg: ProviderConfig{Provider: "gemini"}, wantErr: "api key"},
		{name: "empty provider", cfg: ProviderConfig{}, wantErr: "no provider"},
		{name: "unknown provider", cfg: ProviderConfig{Provider: "clippy"}, wantErr: "unknown provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := SelectBrain(tc.cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := client.Caps().LocalOnly; got != tc.localOnly {
				t.Fatalf("LocalOnly = %v, want %v", got, tc.localOnly)
			}
		})
	}
}

// The unknown-provider error names every provider SelectBrain accepts, so an
// operator who fat-fingers a name sees the full menu.
func TestUnknownProviderErrorListsEverySupportedProvider(t *testing.T) {
	_, err := SelectBrain(ProviderConfig{Provider: "clippy"})
	if err == nil {
		t.Fatal("unknown provider must error")
	}
	for _, want := range []string{"fake", "anthropic", "ollama", "vllm", "openai_compatible", "openai", "gemini"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("unknown-provider error %q omits %q", err.Error(), want)
		}
	}
}
