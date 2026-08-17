// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// noCloudKeys is the fail-closed lookup: every provider's BYOK key is unset.
//
// It replaces a helper that used to clear the four variables with t.Setenv, and
// the reason that helper existed is the reason keys are now injected — a real
// key in the developer's shell could turn a fail-closed case green, and nothing
// about the test said so. A lookup this test constructs cannot be influenced by
// the shell it runs in.
func noCloudKeys() config.Lookup { return config.Static(nil) }

// cloudKeyFor supplies one provider's key and nothing else, so a case that
// binds anthropic proves anthropic resolved its own variable rather than
// borrowing another provider's.
func cloudKeyFor(provider, key string) config.Lookup {
	if env := cloudKeyEnv[provider]; env != "" {
		return config.Static(map[string]string{env: key})
	}
	return noCloudKeys()
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
			keys := noCloudKeys()
			if tc.env != "" {
				keys = cloudKeyFor(tc.cfg.Provider, tc.env)
			}
			client, err := SelectBrain(tc.cfg, keys)
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
	client, err := SelectBrain(ProviderConfig{Provider: "gemini", Model: "gemini-x"},
		cloudKeyFor(providerGemini, "env-gemini-key"))
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
	_, err := SelectBrain(ProviderConfig{Provider: "gemini", Model: "m"}, noCloudKeys())
	if err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Fatalf("missing-key error must name GEMINI_API_KEY, got %v", err)
	}
}

// The unknown-provider error names every provider SelectBrain accepts, so an
// operator who fat-fingers a name sees the full menu.
func TestUnknownProviderErrorListsEverySupportedProvider(t *testing.T) {
	_, err := SelectBrain(ProviderConfig{Provider: "clippy"}, noCloudKeys())
	if err == nil {
		t.Fatal("unknown provider must error")
	}
	for _, want := range []string{"fake", "anthropic", "ollama", "vllm", "openai_compatible", "openai", "gemini"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("unknown-provider error %q omits %q", err.Error(), want)
		}
	}
}

// allCloudKeys supplies every provider's BYOK key, for the many tests whose
// subject is something else entirely — attachment carriage, narrowing, payload
// shape — and which need only that the binding they use resolves.
func allCloudKeys() config.Lookup {
	keys := make(map[string]string, len(cloudKeyEnv))
	for _, env := range cloudKeyEnv {
		keys[env] = "k"
	}
	return config.Static(keys)
}
