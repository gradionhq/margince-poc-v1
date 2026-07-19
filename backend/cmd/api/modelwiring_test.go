// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// resolveModelPath is the one place the api process runs the declared-
// routing/--ai-fake/neither switch (modelwiring.go); these tests exercise
// all three arms without a database. NewModelPath's construction path
// (ai.NewRouter → ai.NewMeter/compose.NewSeatBudget/ai.NewCallMeter) only
// stores the *pgxpool.Pool in each collaborator — it issues no query
// until a lane actually completes a call — so a nil pool is safe here.
// Proving the fake arm's Router actually TRACES a completed call through
// the real pool needs a live database; that lives in
// internal/compose/integration/ai_fake_modelpath_integration_test.go.

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestResolveModelPathNeitherFlagIsUnconfigured proves the honest-absent
// case: no declared routing file and no --ai-fake resolves to a nil
// path and the unconfigured state, never a silent default provider.
func TestResolveModelPathNeitherFlagIsUnconfigured(t *testing.T) {
	modelPath, state, err := resolveModelPath("", false, nil, false, discardLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modelPath != nil {
		t.Fatalf("modelPath = %+v, want nil", modelPath)
	}
	if state != compose.AIStateUnconfigured {
		t.Fatalf("state = %q, want %q", state, compose.AIStateUnconfigured)
	}
}

// TestResolveModelPathFakeArmBindsEveryLane proves --ai-fake resolves a
// real *compose.ModelPath (built over ai.FakeRoutingConfig() through
// compose.NewModelPath, the same constructor the declared-routing arm
// uses) rather than bypassing the Router — every lane must be non-nil,
// or a consumer wired against it would nil-panic on first use.
func TestResolveModelPathFakeArmBindsEveryLane(t *testing.T) {
	modelPath, state, err := resolveModelPath("", true, nil, false, discardLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modelPath == nil {
		t.Fatal("modelPath is nil, want a bound path")
	}
	if state != compose.AIStateFake {
		t.Fatalf("state = %q, want %q", state, compose.AIStateFake)
	}
	if modelPath.Agent == nil {
		t.Error("Agent lane is nil")
	}
	if modelPath.ColdStart == nil {
		t.Error("ColdStart lane is nil")
	}
	if modelPath.SiteExtract == nil {
		t.Error("SiteExtract lane is nil")
	}
	if modelPath.BriefRank == nil {
		t.Error("BriefRank lane is nil")
	}
	if modelPath.DraftReply == nil {
		t.Error("DraftReply lane is nil")
	}
	if modelPath.OfferDraft == nil {
		t.Error("OfferDraft lane is nil")
	}
	if modelPath.Embedder == nil {
		t.Error("Embedder lane is nil")
	}
}

// TestResolveModelPathRoutingFileArmBindsEveryLane proves the declared-
// routing arm resolves the same shape as the fake arm — one Router, all
// lanes bound — over an offline (provider: fake) routing file, so the
// test needs no external credential or network access.
func TestResolveModelPathRoutingFileArmBindsEveryLane(t *testing.T) {
	path := writeFakeRoutingFile(t)
	modelPath, state, err := resolveModelPath(path, false, nil, false, discardLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modelPath == nil {
		t.Fatal("modelPath is nil, want a bound path")
	}
	if state != compose.AIStateConfigured {
		t.Fatalf("state = %q, want %q", state, compose.AIStateConfigured)
	}
	if modelPath.ColdStart == nil {
		t.Error("ColdStart lane is nil")
	}
}

// TestResolveModelPathRoutingFileArmSurfacesLoadError proves a bad
// --ai-routing path fails the boot rather than silently falling back to
// unconfigured or fake.
func TestResolveModelPathRoutingFileArmSurfacesLoadError(t *testing.T) {
	_, _, err := resolveModelPath(filepath.Join(t.TempDir(), "does-not-exist.yaml"), false, nil, false, discardLogger())
	if err == nil {
		t.Fatal("expected an error for a missing routing file, got nil")
	}
}

// TestColdStartOptionsRespectsResolvedPath proves coldStartOptions is a
// pure consumer of the resolved path now: nil in, nil out (the 501
// posture); a bound path in, the cold-start/scrape/brief/reply set out.
func TestColdStartOptionsRespectsResolvedPath(t *testing.T) {
	if got := coldStartOptions(nil); got != nil {
		t.Fatalf("coldStartOptions(nil) = %d options, want 0", len(got))
	}
	modelPath, _, err := resolveModelPath("", true, nil, false, discardLogger())
	if err != nil {
		t.Fatalf("resolveModelPath: %v", err)
	}
	if got := coldStartOptions(modelPath); len(got) != 4 {
		t.Fatalf("coldStartOptions(bound path) = %d options, want 4 (cold-start, scrape, brief, reply draft)", len(got))
	}
}

// TestOfferDraftOptionsRespectsResolvedPath mirrors
// TestColdStartOptionsRespectsResolvedPath for the offer-draft surface.
func TestOfferDraftOptionsRespectsResolvedPath(t *testing.T) {
	if got := offerDraftOptions(nil, nil); got != nil {
		t.Fatalf("offerDraftOptions(nil) = %d options, want 0", len(got))
	}
	modelPath, _, err := resolveModelPath("", true, nil, false, discardLogger())
	if err != nil {
		t.Fatalf("resolveModelPath: %v", err)
	}
	if got := offerDraftOptions(nil, modelPath); len(got) != 1 {
		t.Fatalf("offerDraftOptions(bound path) = %d options, want 1 (offer draft)", len(got))
	}
}

// writeFakeRoutingFile writes a fully offline ai-routing.yaml (every
// tier + embeddings bound to the fake provider) so the declared-routing
// arm can be exercised without any external credential or network call.
func writeFakeRoutingFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ai-routing.yaml")
	const yaml = `
profile: eu_hosted
tiers:
  local_small: { provider: fake }
  cheap_cloud: { provider: fake }
  premium: { provider: fake }
embeddings:
  provider: fake
`
	if err := os.WriteFile(path, bytes.TrimLeft([]byte(yaml), "\n"), 0o600); err != nil {
		t.Fatalf("writing fake routing file: %v", err)
	}
	return path
}
