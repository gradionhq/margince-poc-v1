// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"os"
	"path/filepath"
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

// The shipped default config must always load — a deployment that
// starts from the repo file starts green.
func TestShippedDefaultRoutingParses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "ai-routing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseRouting(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != ProfileEUHosted {
		t.Fatalf("shipped default should be eu_hosted, got %s", cfg.Profile)
	}
}
