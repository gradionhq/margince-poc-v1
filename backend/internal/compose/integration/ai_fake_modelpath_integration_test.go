// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// --ai-fake now rides the real Router (compose.NewModelPath over
// ai.FakeRoutingConfig()) instead of compose.FakeModelPath's direct
// client wiring — this proves the fake arm gets everything the real
// arm gets: routing, metering, and the ai_call trace row. A unit test
// cannot observe this (Router.Complete refuses to run outside a
// workspace context and its trace write goes through the real pool),
// so it lives here against a real migrated database.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestAIFakeModelPathRidesRouterAndTraces(t *testing.T) {
	e := Setup(t)
	modelPath, err := compose.NewModelPath(ai.FakeRoutingConfig(), e.Pool, false, nil)
	if err != nil {
		t.Fatalf("NewModelPath(FakeRoutingConfig()): %v", err)
	}

	ctx := e.Admin()
	resp, err := modelPath.ColdStart.Complete(ctx, model.Request{
		System:   "test",
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ColdStart.Complete: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("fake completion returned an empty response")
	}

	// The Router traces every served call (router.go's serveCompletion) —
	// riding NewModelPath means the fake arm writes the SAME ai_call row
	// the production Router path does, unlike FakeModelPath's direct
	// client wiring which bypasses the Router (and its trace) entirely.
	if n := e.WsCount(t, `SELECT count(*) FROM ai_call WHERE task = 'cold_start' AND provider = 'fake'`); n != 1 {
		t.Fatalf("ai_call rows for the fake cold_start call = %d, want 1", n)
	}
}

// TestAIFakeModelPathBindsEveryLane proves NewModelPath(FakeRoutingConfig())
// leaves no lane nil — the property resolveModelPath's callers (and the
// worker's selectModelPath) depend on to safely wire every AI surface
// under --ai-fake, not just whichever tier a partially-bound config left
// reachable.
func TestAIFakeModelPathBindsEveryLane(t *testing.T) {
	e := Setup(t)
	modelPath, err := compose.NewModelPath(ai.FakeRoutingConfig(), e.Pool, false, nil)
	if err != nil {
		t.Fatalf("NewModelPath(FakeRoutingConfig()): %v", err)
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

	// Embedder lane also traces through the same fake-backed Router.
	if _, err := modelPath.Embedder.Embed(e.Admin(), model.EmbedRequest{Inputs: []string{"x"}}); err != nil {
		t.Fatalf("Embedder.Embed: %v", err)
	}
}
