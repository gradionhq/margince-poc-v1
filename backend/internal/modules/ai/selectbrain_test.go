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
