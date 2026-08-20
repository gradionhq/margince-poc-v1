// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The re-embed is ONE job row that rebuilds the whole corpus. It was one row
// per live tenant until phase D un-scoped the embeddable entities (ADR-0091
// §8), after which every one of those rows walked the SAME table and all but
// the first found each row already fresh at the run's identity. What the suites
// here pin is what survived the collapse: the marker the confirm endpoint gates
// on is claimed by exactly one run, and it comes back on every ending the pass
// has — including the two that never write anything.

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/compose/integration/jobtest"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// reembedFleetEnv is one search harness plus a claimed run over a named fleet.
type reembedFleetEnv struct {
	*SearchEnv
	embedder search.Embedder
	identity string
	run      ids.UUID
}

// setupReembedFleet seeds the marker, claims a run under the fake embed lane's
// identity, and returns the pieces a fan-out scenario drives. The claim happens
// through the store rather than the HTTP confirm because what these scenarios
// exercise is the RUN, and the confirm has its own suite.
func setupReembedFleet(t *testing.T) *reembedFleetEnv {
	t.Helper()
	e := SetupSearch(t)
	ctx := context.Background()
	ApplyRiverSchema(t)
	embedder := fakeEmbedderNamed(t, ai.NewFakeClient(), "model-fanout")
	identity, _ := embedder.EmbedIdentity()
	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	run := ids.NewV7()
	if err := e.Store.ClaimAndEnqueueReembedding(ctx,
		search.ReembedClaim{Run: run, TargetIdentity: identity}, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("claiming the run: %v", err)
	}
	return &reembedFleetEnv{SearchEnv: e, embedder: embedder, identity: identity, run: run}
}

// TestEmbedReindexRebuildsTheCorpusInOnePassAndHandsTheMarkerBack is what is
// left of the fan-out test once the fan-out is gone.
//
// It used to assert one job per LIVE workspace, archived excluded. That shape
// existed because each tenant had a corpus of its own; ADR-0091 §8 phase D took
// the tenant column off every embeddable entity, so the children all walked the
// same rows and all but the first found every row already fresh at the run's
// identity. Counting them was counting jobs whose only effect was to remove
// themselves from a set.
//
// What SURVIVES is what the fan-out was ever for: the corpus gets rebuilt at
// the run's identity, and the marker comes back so the next confirm is not
// refused by a run that ended. Both are asserted here. The archived-workspace
// case goes with the enumeration — there is no per-tenant enqueue left to skip.
func TestEmbedReindexRebuildsTheCorpusInOnePassAndHandsTheMarkerBack(t *testing.T) {
	re := setupReembedFleet(t)
	// A second live workspace and an archived one still exist, deliberately:
	// the pass must not grow work with them, which a fan-out did by
	// construction. Nothing below counts jobs per tenant.
	SeedExtraWorkspace(t, re.Owner, "reindex-second", false)
	SeedExtraWorkspace(t, re.Owner, "reindex-archived", true)

	leadID := re.SeedID(t, `INSERT INTO lead (id, full_name, source, captured_by) VALUES ($1, 'One Pass Lead', 'manual', 'human:x')`)

	runner, completed, failed := jobtest.StartTestJobRunner(t, re.Pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		Embedder:          re.embedder,
	})
	if err := runner.Enqueue(context.Background(), compose.EmbedReindexArgs{Run: re.run, Identity: re.identity}, nil); err != nil {
		t.Fatalf("enqueueing the run: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if !jobtest.AwaitKindOutcome(waitCtx, t, completed, failed, compose.EmbedReindexArgs{}.Kind()) {
		t.Fatal("the re-embed pass failed")
	}

	var model string
	if err := re.Owner.QueryRow(context.Background(),
		`SELECT model FROM embedding WHERE entity_type = 'lead' AND entity_id = $1 AND chunk_ix = 0`,
		leadID).Scan(&model); err != nil {
		t.Fatalf("reading the rebuilt embedding: %v", err)
	}
	if model != re.identity {
		t.Errorf("the lead is embedded under %q, want %q", model, re.identity)
	}

	// The marker is back, which is what lets the next confirm through. A run
	// that finished its work and kept the marker refuses every later reindex
	// until a forced steal an hour away.
	var held *string
	if err := re.Pool.QueryRow(context.Background(),
		`SELECT reembedding_run::text FROM embed_store_binding`).Scan(&held); err != nil {
		t.Fatalf("reading the binding marker: %v", err)
	}
	if held != nil {
		t.Errorf("the marker is still held by run %s after the pass finished — every later confirm is refused until a forced steal", *held)
	}
}

// TestEmbedReindexForceTakesTheMarkerBackFromAWedgedRun wires the store's escape
// hatch to the surface a human actually has. Nothing else clears the marker: a
// restart does not (SeedBinding is ON CONFLICT DO NOTHING), and the release
// cannot be made airtight — a workspace job declares Timeout() == -1, which puts
// it outside River's rescuer at any age, so a child whose process died leaves a
// running row nothing retries or discards and a workspace that never leaves the
// run's set. Without this, that installation answers 409 to every reindex
// forever. `force` on its own must NOT steal, or the escape hatch becomes the
// normal path and two runs fan out over each other.
func TestEmbedReindexForceTakesTheMarkerBackFromAWedgedRun(t *testing.T) {
	router := embedReindexRouter(t, "reindex-wedged-v1")
	e := setupEmbedReindex(t, router)

	if status, _, _ := embedConfirm(t, e, apptest.AnyMap{"force": true}); status != http.StatusAccepted {
		t.Fatalf("first confirm -> %d, want 202", status)
	}
	// A forced confirm while the run is genuinely moving must still be refused:
	// the marker was claimed a moment ago, so nothing here is stale.
	if status, _, problem := embedConfirm(t, e, apptest.AnyMap{"force": true}); status != http.StatusConflict || problem.Code != "reindex_running" {
		t.Fatalf("forced confirm over a live run -> %d %+v, want 409 reindex_running", status, problem)
	}

	// The run's only child was killed outright: its workspace never left the set
	// and the marker has not moved since. Aged rather than waited out — a suite
	// that waited an hour is a suite nobody runs.
	if _, err := e.Owner.Exec(context.Background(),
		`UPDATE embed_store_binding SET updated_at = now() - interval '2 hours' WHERE singleton`); err != nil {
		t.Fatalf("ageing the wedged marker: %v", err)
	}

	if status, _, problem := embedConfirm(t, e, nil); status != http.StatusConflict || problem.Code != "reindex_running" {
		t.Fatalf("bare confirm over a wedged marker -> %d %+v, want 409 — taking a run's marker away is something a human asks for", status, problem)
	}
	status, confirmed, problem := embedConfirm(t, e, apptest.AnyMap{"force": true})
	if status != http.StatusAccepted {
		t.Fatalf("forced confirm over a wedged marker -> %d %+v, want 202 — an installation with no way back answers 409 forever", status, problem)
	}
	if confirmed.Status != "reembedding" {
		t.Fatalf("status after taking the marker over = %q, want reembedding", confirmed.Status)
	}
}

// TestEmbedReindexWithNoLiveWorkspaceHandsTheMarkerBack pins the one path with
// nothing to embed. A deployment whose only workspace is archived has no corpus
// to rebuild, and a run that claimed the marker and then found nothing to bind a
// pass to would retry itself to exhaustion still holding it — refusing every
// later confirm with no job left anywhere to explain why.
func TestEmbedReindexWithNoLiveWorkspaceHandsTheMarkerBack(t *testing.T) {
	re := setupReembedFleet(t)
	if _, err := re.Owner.Exec(context.Background(),
		`UPDATE workspace SET archived_at = now() WHERE id = $1`, re.WS); err != nil {
		t.Fatalf("archiving the only workspace: %v", err)
	}

	// Subscribed to CANCELLED, not completed: a pass with nothing to bind to is
	// permanent for this row, so it stops deliberately rather than finishing —
	// awaiting the wrong terminal state here would hang rather than fail.
	runner, err := compose.NewJobRunner(re.Pool, slog.New(slog.DiscardHandler), compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		Embedder:          re.embedder,
	})
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	sub, cancelSub := runner.SubscribeCancelled()
	defer cancelSub()
	startEmbedReindexRunner(t, runner)
	if err := runner.Enqueue(context.Background(), compose.EmbedReindexArgs{Run: re.run, Identity: re.identity}, nil); err != nil {
		t.Fatalf("enqueueing the run: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	AwaitKindCompleted(waitCtx, t, sub, compose.EmbedReindexArgs{}.Kind())

	populated, status, _, err := re.Store.PopulatedIdentity(context.Background())
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "idle" {
		t.Fatalf("marker status = %q after a run with no live workspace, want idle — nothing will ever come along to release it", status)
	}
	if populated != re.identity {
		t.Fatalf("populated_identity = %q, want %q", populated, re.identity)
	}
}
