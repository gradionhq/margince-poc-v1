// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"path/filepath"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
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

// A fresh install must be able to read an uploaded document without the
// operator configuring anything. The default example binds gemini, whose
// carriage is a fact about its wire and needs no `input:` declaration — but
// "needs none" and "has none" look identical in the file, so the difference is
// asserted here rather than left to a reader of the comment.
//
// Re-pointing the default at a provider whose carriage is model-dependent
// (openai_compatible, vllm) without declaring `input:` would silently ship a
// default install that refuses every document. This is the test that would say so.
func TestTheDefaultExampleCanBeGivenADocumentOutOfTheBox(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "k")
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "k")
	cfg, err := LoadRoutingFile("../../../../config/ai-routing.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// document_extract is the feature that reads one, and its carriage is the
	// intersection over its own ladder — so ask the router, not a single tier.
	router, err := NewRouter(cfg, nil, nil, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if carried := router.AttachmentMIMEs(TaskDocumentExtract); len(carried) == 0 {
		t.Fatal("the default example cannot be given a document: document_extract's ladder declares no carriage")
	} else if !model.CarriesMIME(carried, "image/png") {
		t.Fatalf("a scanned document is the point; carriage = %v", carried)
	}
}

// An OpenRouter example brokers every call through a third-party inference
// provider and sends no provider-routing preference, so none of them may claim
// a residency posture the request path cannot keep. Derived from the tree, so a
// further jurisdiction file inherits the rule instead of needing its own test.
func TestNoOpenRouterExampleClaimsResidencyItCannotKeep(t *testing.T) {
	const pattern = "../../../../config/ai-routing.openrouter*.example.yaml"
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("globbing %s: %v", pattern, err)
	}
	if len(paths) == 0 {
		t.Fatalf("%s matched no file; the OpenRouter examples moved and this gate went vacuous", pattern)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			cfg, err := LoadRoutingFile(path)
			if err != nil {
				t.Fatalf("%s no longer parses: %v", path, err)
			}
			if cfg.Profile != ProfileCloudFrontier {
				t.Fatalf("%s profile = %q, want %q", path, cfg.Profile, ProfileCloudFrontier)
			}
		})
	}
}
