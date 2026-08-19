// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The fleet-wide re-embed is one job row per live tenant. It used to be one row
// for the whole fleet: a workspace that could not write ended the pass there and
// then, so every tenant behind it in the fleet order kept a stale index with no
// row anywhere recording that it had been skipped — and the marker the confirm
// endpoint gates on was left claimed by a run nobody could see the shape of.

import (
	"context"
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

// TestEmbedReindexFansOutOneJobPerLiveWorkspace is the converted shape: the
// dispatcher enqueues one row per LIVE workspace — archived ones excluded,
// because an archived tenant's records are not searched and re-embedding them
// spends model budget building an index nobody queries — each row names its own
// tenant on the wire, and the corpus is rebuilt at the run's identity.
//
// It does NOT assert that a tenant whose writes fail is the only child to fail,
// which is what this test used to be named for. That property no longer holds
// and cannot be made to: an embedding row is keyed on
// (entity_type, entity_id, chunk_ix) alone (phase B) over records that carry no
// tenant at all (phase D), so the corpus is the installation's and whichever
// child runs first rebuilds ALL of it. The second child finds every row already
// fresh at the run's identity, writes nothing, and therefore cannot meet a
// write fault however permanently it is armed. The fan-out itself is what has
// gone redundant; collapsing it is ADR-0091's own next step, and the isolation
// question disappears with it rather than being answered.
func TestEmbedReindexFansOutOneJobPerLiveWorkspace(t *testing.T) {
	re := setupReembedFleet(t)
	healthy := SeedExtraWorkspace(t, re.Owner, "reindex-healthy", false)
	archived := SeedExtraWorkspace(t, re.Owner, "reindex-archived", true)

	// ONE entity, and it belongs to the installation rather than to either
	// tenant: seeding a row "per tenant" would be seeding the same corpus twice
	// under two names.
	leadID := re.SeedID(t, `INSERT INTO lead (id, full_name, source, captured_by) VALUES ($1, 'Fanout Lead', 'manual', 'human:x')`)

	runner, completed, failed := jobtest.StartTestJobRunner(t, re.Pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		Embedder:          re.embedder,
	})
	if err := runner.Enqueue(context.Background(), compose.EmbedReindexArgs{Run: re.run, Identity: re.identity}, nil); err != nil {
		t.Fatalf("enqueueing the run's dispatcher: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	kind := compose.EmbedReindexWorkspaceArgs{}.Kind()
	outcomes := jobtest.AwaitWorkspaceJobOutcomes(waitCtx, t, completed, failed, kind, 2)

	for _, ws := range []ids.UUID{re.WS, healthy} {
		ran, fannedOut := outcomes[ws.String()]
		if !fannedOut {
			t.Errorf("no re-embed ran for workspace %s — a tenant the fan-out skipped keeps a stale index, and no row records that it did not", ws)
			continue
		}
		if !ran {
			t.Errorf("the re-embed for workspace %s failed", ws)
		}
	}
	// The fan-out is only worth its rows if the corpus actually got rebuilt.
	var model string
	if err := re.Owner.QueryRow(context.Background(),
		`SELECT model FROM embedding WHERE entity_type = 'lead' AND entity_id = $1 AND chunk_ix = 0`,
		leadID).Scan(&model); err != nil {
		t.Fatalf("reading the rebuilt embedding: %v", err)
	}
	if model != re.identity {
		t.Errorf("the lead is embedded under %q, want %q", model, re.identity)
	}

	// The archived tenant must have no row at all. This count is fenced on the
	// two outcomes above rather than read early: the fan-out is ONE atomic
	// insert, so any child reporting proves that insert committed — and it
	// carried every workspace the dispatcher enumerated.
	var dispatched int
	if err := re.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'workspace_id' = $2`,
		kind, archived.String()).Scan(&dispatched); err != nil {
		t.Fatalf("counting the archived tenant's re-embed jobs: %v", err)
	}
	if dispatched != 0 {
		t.Errorf("%d %s rows were dispatched for an ARCHIVED workspace — re-embedding records nobody searches spends model budget on work nobody wants", dispatched, kind)
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

// TestEmbedReindexDispatcherWithAnEmptyFleetHandsTheMarkerBack pins the one path
// with no child to release the marker. A deployment whose only workspace is
// archived has nothing to re-embed, and a run that claimed the marker and then
// found nothing to wait on would hold it forever — refusing every later confirm
// with no job anywhere to explain why.
func TestEmbedReindexDispatcherWithAnEmptyFleetHandsTheMarkerBack(t *testing.T) {
	re := setupReembedFleet(t)
	if _, err := re.Owner.Exec(context.Background(),
		`UPDATE workspace SET archived_at = now() WHERE id = $1`, re.WS); err != nil {
		t.Fatalf("archiving the only workspace: %v", err)
	}

	runner, completed, _ := jobtest.StartTestJobRunner(t, re.Pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		Embedder:          re.embedder,
	})
	if err := runner.Enqueue(context.Background(), compose.EmbedReindexArgs{Run: re.run, Identity: re.identity}, nil); err != nil {
		t.Fatalf("enqueueing the run's dispatcher: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	jobtest.AwaitKindsCompleted(waitCtx, t, completed, compose.EmbedReindexArgs{}.Kind())

	populated, status, _, err := re.Store.PopulatedIdentity(context.Background())
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "idle" {
		t.Fatalf("marker status = %q after a run over an empty fleet, want idle — nothing will ever come along to release it", status)
	}
	if populated != re.identity {
		t.Fatalf("populated_identity = %q, want %q", populated, re.identity)
	}
}
