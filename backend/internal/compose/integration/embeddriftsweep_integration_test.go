// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The embed drift sweep (ADR-0069 §3a, SEARCH-AC-13): the at-least-once
// bus loses embed events — an acked event whose embedding write never
// landed — and those entities must be healed WITHOUT an operator confirm,
// because re-embedding them is the same spend class as the event lane
// that missed them. The sweep runs only under a matched identity with no
// reindex job live: the binding-change case keeps its preview→confirm
// human consent and is exactly what the sweep must never touch.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestSweepWorkspaceEmbeddingDriftHealsIdentityMatchedGaps seeds two people under
// a matched binding — one embedded, one whose embed event was "lost" (no
// embedding row at all) — and proves the sweep embeds exactly the missing
// one: one model call, pending drops to 0, and the binding marker is not
// touched (the sweep is not a reindex and must never stamp the marker).
func TestSweepWorkspaceEmbeddingDriftHealsIdentityMatchedGaps(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	embedder := fakeEmbedderNamed(t, fake, "model-current")
	identity, _ := embedder.EmbedIdentity()

	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	embeddedID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Embedded Person', 'manual', 'human:x')`)
	if _, err := e.Store.UpsertEmbedding(e.Admin(), "person", embeddedID, "Embedded Person", embedder); err != nil {
		t.Fatalf("seeding the already-embedded baseline: %v", err)
	}
	lostID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Lost Event Person', 'manual', 'human:x')`)
	baselineCalls := len(fake.Calls())

	healed, err := e.Store.SweepWorkspaceEmbeddingDrift(ctx, ids.From[ids.WorkspaceKind](e.WS), embedder)
	if err != nil {
		t.Fatalf("SweepWorkspaceEmbeddingDrift: %v", err)
	}
	if healed != 1 {
		t.Fatalf("healed = %d, want 1 (only the entity whose event was lost)", healed)
	}
	if calls := len(fake.Calls()) - baselineCalls; calls != 1 {
		t.Fatalf("sweep made %d embed calls, want 1 — already-current rows are not even enumerated", calls)
	}
	if got := e.storedEmbeddingModel(t, lostID); got != identity {
		t.Fatalf("lost entity's embedding model = %q, want %q", got, identity)
	}

	pending, err := e.Store.EntitiesPending(ctx, identity)
	if err != nil {
		t.Fatalf("EntitiesPending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("EntitiesPending = %d, want 0 after the sweep", pending)
	}

	populated, status, _, err := e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != identity || status != "idle" {
		t.Fatalf("sweep touched the binding marker: populated=%q status=%q, want %q/idle", populated, status, identity)
	}

	// A second pass over a healed store is a no-op — the sweep is safe to
	// tick forever.
	healed, err = e.Store.SweepWorkspaceEmbeddingDrift(ctx, ids.From[ids.WorkspaceKind](e.WS), embedder)
	if err != nil {
		t.Fatalf("second SweepWorkspaceEmbeddingDrift: %v", err)
	}
	if healed != 0 {
		t.Fatalf("second sweep healed = %d, want 0", healed)
	}
}

// TestSweepWorkspaceEmbeddingDriftRefusesTheBindingChangeCase proves the sweep
// no-ops — zero model calls, the missing row stays missing — when the
// configured identity differs from what the store is populated under:
// that state is the operator's preview→confirm rebuild to trigger, never
// the sweep's (ADR-0069 §3a keeps the consent gate exactly there).
func TestSweepWorkspaceEmbeddingDriftRefusesTheBindingChangeCase(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	embedder := fakeEmbedderNamed(t, fake, "model-new")

	if err := e.Store.SeedBinding(ctx, "provider/model-old@1024"); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Unswept Person', 'manual', 'human:x')`)

	// The refusal only proves anything if there was real pending work to
	// refuse — healed==0 over an empty pending set is vacuous.
	identity, _ := embedder.EmbedIdentity()
	pending, err := e.Store.EntitiesPending(ctx, identity)
	if err != nil {
		t.Fatalf("EntitiesPending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("test setup: EntitiesPending = %d, want 1", pending)
	}

	healed, err := e.Store.SweepWorkspaceEmbeddingDrift(ctx, ids.From[ids.WorkspaceKind](e.WS), embedder)
	if err != nil {
		t.Fatalf("SweepWorkspaceEmbeddingDrift: %v", err)
	}
	if healed != 0 {
		t.Fatalf("healed = %d, want 0 under a changed binding", healed)
	}
	if calls := len(fake.Calls()); calls != 0 {
		t.Fatalf("a binding-change sweep must not call the embedder, got %d calls", calls)
	}
}

// TestSweepWorkspaceEmbeddingDriftWaitsOutARunningReindex proves the sweep no-ops
// while the binding marker reads 'reembedding': the fleet-wide job owns
// the store for that window, and the sweep re-running underneath it would
// double-walk the same pending set for nothing.
func TestSweepWorkspaceEmbeddingDriftWaitsOutARunningReindex(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	embedder := fakeEmbedderNamed(t, fake, "model-current")
	identity, _ := embedder.EmbedIdentity()

	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, search.ReembedClaim{Run: ids.NewV7(), TargetIdentity: identity}, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("ClaimAndEnqueueReembedding: %v", err)
	}
	e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Mid Reindex Person', 'manual', 'human:x')`)

	// The wait-out only proves anything if the sweep had real pending work
	// it chose not to touch — healed==0 over an empty set is vacuous.
	pending, err := e.Store.EntitiesPending(ctx, identity)
	if err != nil {
		t.Fatalf("EntitiesPending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("test setup: EntitiesPending = %d, want 1", pending)
	}

	healed, err := e.Store.SweepWorkspaceEmbeddingDrift(ctx, ids.From[ids.WorkspaceKind](e.WS), embedder)
	if err != nil {
		t.Fatalf("SweepWorkspaceEmbeddingDrift: %v", err)
	}
	if healed != 0 {
		t.Fatalf("healed = %d, want 0 while a reindex job is live", healed)
	}
	if calls := len(fake.Calls()); calls != 0 {
		t.Fatalf("a mid-reindex sweep must not call the embedder, got %d calls", calls)
	}
}
