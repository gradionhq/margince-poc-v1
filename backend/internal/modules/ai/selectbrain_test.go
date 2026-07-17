// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"strings"
	"testing"
)

// clearCloudKeyEnv unsets every cloud provider's BYOK env var for the test, so
// a real key in the developer's shell can't turn a fail-closed case green.
func clearCloudKeyEnv(t *testing.T) {
	t.Helper()
	for _, env := range cloudKeyEnv {
		t.Setenv(env, "")
	}
}

// The B-EP06.2 acceptance shape: "offline fake ↔ API key ↔ local, one line" —
// each provider is one config value away, and a cloud key comes from the
// environment (never the routing file).
func TestSelectBrainOneLinePerProvider(t *testing.T) {
	cases := []struct {
		name      string
		cfg       ProviderConfig
		env       string // value for this provider's cloud key env var; "" leaves it unset
		wantErr   string
		localOnly bool
	}{
		{name: "offline fake", cfg: ProviderConfig{Provider: "fake"}, localOnly: true},
		{name: "cloud byok", cfg: ProviderConfig{Provider: "anthropic", Model: "claude-x"}, env: "k", localOnly: false},
		{name: "local", cfg: ProviderConfig{Provider: "ollama", Model: "gemma3"}, localOnly: true},
		{name: "cloud without key fails closed", cfg: ProviderConfig{Provider: "anthropic"}, wantErr: "api key"},
		{name: "openai-compat cloud byok", cfg: ProviderConfig{Provider: "openai_compatible", BaseURL: "https://api.mistral.ai", Model: "mistral-small-latest"}, env: "k", localOnly: false},
		{name: "openai-compat without key fails closed", cfg: ProviderConfig{Provider: "openai_compatible", BaseURL: "https://x"}, wantErr: "api key"},
		{name: "openai-compat without base_url fails closed", cfg: ProviderConfig{Provider: "openai_compatible"}, env: "k", wantErr: "base_url"},
		{name: "openai native byok", cfg: ProviderConfig{Provider: "openai", Model: "gpt-x"}, env: "k", localOnly: false},
		{name: "openai without key fails closed", cfg: ProviderConfig{Provider: "openai"}, wantErr: "api key"},
		{name: "gemini native byok", cfg: ProviderConfig{Provider: "gemini", Model: "gemini-x"}, env: "k", localOnly: false},
		{name: "gemini without key fails closed", cfg: ProviderConfig{Provider: "gemini"}, wantErr: "api key"},
		{name: "empty provider", cfg: ProviderConfig{}, wantErr: "no provider"},
		{name: "unknown provider", cfg: ProviderConfig{Provider: "clippy"}, wantErr: "unknown provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearCloudKeyEnv(t)
			if tc.env != "" {
				if envVar := cloudKeyEnv[tc.cfg.Provider]; envVar != "" {
					t.Setenv(envVar, tc.env)
				}
			}
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

// A cloud binding needs no api_key in the routing file — the key is read from
// the provider's conventional environment variable (secrets live in env).
func TestCloudKeyResolvesFromEnvWhenConfigOmitsIt(t *testing.T) {
	clearCloudKeyEnv(t)
	t.Setenv("GEMINI_API_KEY", "env-gemini-key")
	client, err := SelectBrain(ProviderConfig{Provider: "gemini", Model: "gemini-x"})
	if err != nil {
		t.Fatalf("gemini must resolve its key from GEMINI_API_KEY: %v", err)
	}
	gc, ok := client.(*geminiClient)
	if !ok || gc.apiKey != "env-gemini-key" {
		t.Fatalf("env key not applied: %+v", client)
	}
}

// The fail-closed error names the env var to set, so the fix is obvious.
func TestMissingKeyErrorNamesTheEnvVar(t *testing.T) {
	clearCloudKeyEnv(t)
	_, err := SelectBrain(ProviderConfig{Provider: "gemini", Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Fatalf("missing-key error must name GEMINI_API_KEY, got %v", err)
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
