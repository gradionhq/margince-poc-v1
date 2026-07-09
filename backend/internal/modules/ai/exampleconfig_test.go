// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "testing"

// The annotated example shipped for operators must always satisfy the real
// parser — a schema change here without a matching edit there would hand
// every new deployment a config that fails at boot.
func TestShippedExampleRoutingConfigParses(t *testing.T) {
	cfg, err := LoadRoutingFile("../../../../config/ai-routing.example.yaml")
	if err != nil {
		t.Fatalf("config/ai-routing.example.yaml no longer parses: %v", err)
	}
	if cfg.Profile != ProfileEUHosted {
		t.Fatalf("example profile = %q, want the default posture %q", cfg.Profile, ProfileEUHosted)
	}
	if len(cfg.Tiers) == 0 {
		t.Fatal("example binds no tiers")
	}
}
