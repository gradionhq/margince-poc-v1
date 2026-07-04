package ai

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// A live one-shot against the real API, run only where a developer has
// dropped a key at ~/.margince/anthropic_key (or MARGINCE_ANTHROPIC_KEY).
// Skipping is fine here — this is a reachability probe, not a security
// gate; every behavioral property is covered by the httptest suite.
func TestAnthropicLiveSmoke(t *testing.T) {
	key := os.Getenv("MARGINCE_ANTHROPIC_KEY")
	if key == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			// The path is derived from the trusted home dir, not input.
			raw, err := os.ReadFile(filepath.Join(home, ".margince", "anthropic_key")) // #nosec G304
			if err == nil {
				key = strings.TrimSpace(string(raw))
			}
		}
	}
	if key == "" {
		t.Skip("no Anthropic key configured; live smoke skipped")
	}
	client, err := SelectBrain(ProviderConfig{Provider: "anthropic", Model: "claude-haiku-4-5-20251001", APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Complete(ctx, model.Request{
		System:         "Answer with exactly one word.",
		Messages:       []model.Message{{Role: "user", Content: "Say the word margin."}},
		MaxTokens:      16,
		SecretStripper: NewSecretStripper(),
	})
	if err != nil {
		t.Fatalf("live Complete: %v", err)
	}
	if resp.Text == "" || resp.OutputTokens == 0 {
		t.Fatalf("empty live response: %+v", resp)
	}
	t.Logf("live smoke ok: %q (in=%d out=%d)", resp.Text, resp.InputTokens, resp.OutputTokens)
}
