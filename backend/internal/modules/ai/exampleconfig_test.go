// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"path/filepath"
	"testing"
)

// exampleRoutingGlob matches every annotated routing example the tree ships.
// Deriving the list from the tree rather than naming one path is what keeps a
// newly-added example gated: a second file nobody registers is a config an
// operator copies and boots into a parse error.
const exampleRoutingGlob = "../../../../config/ai-routing*.example.yaml"

// exampleRoutingFiles returns every shipped example, failing when the glob
// matches nothing — a rename that empties it would leave every gate built on
// it passing vacuously.
func exampleRoutingFiles(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(exampleRoutingGlob)
	if err != nil {
		t.Fatalf("globbing %s: %v", exampleRoutingGlob, err)
	}
	if len(paths) == 0 {
		t.Fatalf("%s matched no file; the examples moved or were renamed, and this gate went vacuous", exampleRoutingGlob)
	}
	return paths
}

// Every annotated example shipped for operators must satisfy the real parser —
// a schema change without a matching edit there would hand a new deployment a
// config that fails at boot.
func TestEveryShippedExampleRoutingConfigParses(t *testing.T) {
	for _, path := range exampleRoutingFiles(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			cfg, err := LoadRoutingFile(path)
			if err != nil {
				t.Fatalf("%s no longer parses: %v", path, err)
			}
			if len(cfg.Tiers) == 0 {
				t.Fatalf("%s binds no tiers", path)
			}
		})
	}
}

// The Gemini example is the default a fresh install is seeded with, so its
// profile is pinned here rather than in the loop above: eu_hosted is a claim
// about that binding specifically, and no other example inherits it.
func TestTheDefaultExampleRoutingConfigDeclaresTheEUHostedPosture(t *testing.T) {
	cfg, err := LoadRoutingFile("../../../../config/ai-routing.example.yaml")
	if err != nil {
		t.Fatalf("config/ai-routing.example.yaml no longer parses: %v", err)
	}
	if cfg.Profile != ProfileEUHosted {
		t.Fatalf("default example profile = %q, want the seeded posture %q", cfg.Profile, ProfileEUHosted)
	}
}

// The OpenRouter example brokers every call through a third-party inference
// provider and sends no provider-routing preference, so it must not claim a
// residency posture the request path cannot keep.
func TestTheOpenRouterExampleDeclaresNoResidencyItCannotKeep(t *testing.T) {
	cfg, err := LoadRoutingFile("../../../../config/ai-routing.openrouter.example.yaml")
	if err != nil {
		t.Fatalf("config/ai-routing.openrouter.example.yaml no longer parses: %v", err)
	}
	if cfg.Profile != ProfileCloudFrontier {
		t.Fatalf("openrouter example profile = %q, want %q", cfg.Profile, ProfileCloudFrontier)
	}
}
