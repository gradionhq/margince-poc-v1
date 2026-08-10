// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The resumable corpus re-embed routine (ADR-0068 design §5.6-swap v7,
// Task 10): ReembedWorkspace re-embeds every live entity of ONE workspace
// under a target identity, is free to re-run (UpsertEmbedding's
// content-hash + identity skip-compare makes an already-current row cost
// no model call), and refuses to run at all — via ErrIdentityDrift — when
// the embedder compose actually injected disagrees with the job's target
// identity. Beside it, the binding marker's own run lifecycle: a claimed
// run holds the marker until the LAST workspace in its pending set is
// finished with, whatever outcome each of them reached.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestReembedWorkspaceReembedsAllLiveEntitiesAndIsResumable seeds 3 people
// under a stale identity, then proves a single ReembedWorkspace call under
// a NEW identity re-embeds all 3 (their stored model becomes the new
// identity) and reads EntitiesPending == 0 afterward. A SECOND pass over
// the same identity must cost zero additional embed calls — the
// resumability property Task 6's skip-compare exists to provide.
func TestReembedWorkspaceReembedsAllLiveEntitiesAndIsResumable(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	staleEmbedder := fakeEmbedderNamed(t, fake, "model-stale")
	newEmbedder := fakeEmbedderNamed(t, fake, "model-new")
	staleIdentity, _ := staleEmbedder.EmbedIdentity()
	newIdentity, _ := newEmbedder.EmbedIdentity()
	if staleIdentity == newIdentity {
		t.Fatalf("test setup produced identical identities %q — no swap exercised", staleIdentity)
	}

	if err := e.Store.SeedBinding(ctx, staleIdentity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	names := []string{"Reembed One", "Reembed Two", "Reembed Three"}
	personIDs := make([]ids.UUID, len(names))
	for i, name := range names {
		id := e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, $3, 'manual', 'human:x')`, name)
		if _, err := e.Store.UpsertEmbedding(e.Admin(), "person", id, name, staleEmbedder); err != nil {
			t.Fatalf("seeding the stale-identity baseline for %s: %v", name, err)
		}
		personIDs[i] = id
	}
	baselineCalls := len(fake.Calls())

	wsID := ids.From[ids.WorkspaceKind](e.WS)
	run := ids.NewV7()
	if err := e.Store.ReembedWorkspace(ctx, search.ReembedPass{Run: run, Identity: newIdentity}, wsID, newEmbedder); err != nil {
		t.Fatalf("ReembedWorkspace: %v", err)
	}

	for i, id := range personIDs {
		if got := e.storedEmbeddingModel(t, id); got != newIdentity {
			t.Fatalf("person[%d] model = %q, want %q (must have been re-embedded under the new identity)", i, got, newIdentity)
		}
	}

	firstPassCalls := len(fake.Calls()) - baselineCalls
	if firstPassCalls != len(names) {
		t.Fatalf("first ReembedWorkspace made %d embed calls, want %d (one per live entity)", firstPassCalls, len(names))
	}

	pending, err := e.Store.EntitiesPending(ctx, newIdentity)
	if err != nil {
		t.Fatalf("EntitiesPending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("EntitiesPending = %d, want 0 after a clean re-embed", pending)
	}

	// Resumability: nothing changed since the first pass, so every row is
	// already current under newIdentity — the skip-compare inside
	// UpsertEmbedding must short-circuit before ever calling the embedder.
	if err := e.Store.ReembedWorkspace(ctx, search.ReembedPass{Run: run, Identity: newIdentity}, wsID, newEmbedder); err != nil {
		t.Fatalf("second ReembedWorkspace: %v", err)
	}
	secondPassCalls := len(fake.Calls()) - baselineCalls - firstPassCalls
	if secondPassCalls != 0 {
		t.Fatalf("second ReembedWorkspace made %d embed calls, want 0 (a resumed/re-run pass must be free)", secondPassCalls)
	}
}

// failEmbeddingWritesFor makes every embedding write inside ONE tenant raise,
// leaving every other tenant's untouched — the fault that used to end the whole
// fleet pass and now ends one workspace's.
//
// It is dropped in cleanup: the integration lane resets rows between tests but
// keeps the schema, so a surviving trigger would break every later suite that
// embeds anything in this workspace.
func failEmbeddingWritesFor(t *testing.T, owner *pgx.Conn, ws ids.UUID) {
	t.Helper()
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `
		CREATE OR REPLACE FUNCTION embedding_write_fault() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
		  RAISE EXCEPTION 'embedding write fault injection';
		END $$`); err != nil {
		t.Fatalf("creating the fault-injection function: %v", err)
	}
	// Registered before the trigger is armed, not after both: a failure to arm
	// would otherwise leave the function behind, which is the leak this cleanup
	// exists to prevent. Cleanups run LIFO, so the trigger still drops first.
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DROP FUNCTION embedding_write_fault()`); err != nil {
			t.Errorf("dropping the fault-injection function: %v", err)
		}
	})
	// CREATE TRIGGER takes no bind parameters, so the tenant is interpolated;
	// it is a UUID rendered by ids.UUID.String(), never caller text.
	if _, err := owner.Exec(ctx, `
		CREATE TRIGGER embedding_write_fault_trigger
		BEFORE INSERT OR UPDATE ON embedding
		FOR EACH ROW WHEN (NEW.workspace_id = '`+ws.String()+`'::uuid)
		EXECUTE FUNCTION embedding_write_fault()`); err != nil {
		t.Fatalf("arming the fault-injection trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DROP TRIGGER embedding_write_fault_trigger ON embedding`); err != nil {
			t.Errorf("dropping the fault-injection trigger: %v", err)
		}
	})
}

// TestReembedWorkspaceCostsOnlyTheWorkspaceThatCannotWrite is the
// characterization the split earns. The fleet pass this replaces walked every
// tenant inside one row and RETURNED on the first that failed, so a single
// tenant's transient write fault left every workspace behind it in the fleet
// order un-re-embedded — silently, with no row anywhere saying so.
func TestReembedWorkspaceCostsOnlyTheWorkspaceThatCannotWrite(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	embedder := fakeEmbedderNamed(t, fake, "model-isolation")
	identity, _ := embedder.EmbedIdentity()
	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	healthy := SeedExtraWorkspace(t, e.Owner, "reembed-healthy", false)
	e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Faulted Tenant Person', 'manual', 'human:x')`)
	healthyPersonID := ids.NewV7()
	if _, err := e.Owner.Exec(ctx, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Healthy Tenant Person', 'manual', 'human:x')`,
		healthyPersonID, healthy); err != nil {
		t.Fatalf("seeding the healthy tenant's person: %v", err)
	}
	failEmbeddingWritesFor(t, e.Owner, e.WS)

	if err := e.Store.ReembedWorkspace(ctx, search.ReembedPass{Run: ids.NewV7(), Identity: identity}, ids.From[ids.WorkspaceKind](e.WS), embedder); err == nil {
		t.Fatal("a workspace whose embedding writes could not land reported success — nothing records that its corpus was never rebuilt")
	}

	// The fault is one tenant's, and the pass now takes the tenant it is given.
	if err := e.Store.ReembedWorkspace(ctx, search.ReembedPass{Run: ids.NewV7(), Identity: identity}, ids.From[ids.WorkspaceKind](healthy), embedder); err != nil {
		t.Fatalf("the healthy tenant's pass, while the victim's is faulted: %v", err)
	}
	// Read outside e.WS: SearchEnv.storedEmbeddingModel is pinned to the
	// harness's own workspace, and a cross-tenant claim has to read the tenant
	// it is making the claim about.
	var model string
	if err := e.Owner.QueryRow(ctx,
		`SELECT model FROM embedding WHERE workspace_id = $1 AND entity_type = 'person' AND entity_id = $2 AND chunk_ix = 0`,
		healthy, healthyPersonID).Scan(&model); err != nil {
		t.Fatalf("reading the healthy tenant's embedding: %v", err)
	}
	if model != identity {
		t.Fatalf("the healthy tenant's person is embedded under %q, want %q", model, identity)
	}
}

// TestReembedWorkspaceIdentityDriftCancelsWithoutTouchingRows proves the
// entry guard fires — and touches NOTHING — when the embedder compose
// actually injected no longer agrees with the job's own target identity:
// an operator swapped the live embed binding after this job was
// enqueued. The worker maps ErrIdentityDrift to river.JobCancel so a stale
// job cancels cleanly instead of burning its ladder against an identity
// nothing serves anymore.
func TestReembedWorkspaceIdentityDriftCancelsWithoutTouchingRows(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	embedder := fakeEmbedderNamed(t, fake, "model-current")

	const markerIdentity = "stale-marker-identity"
	const staleRowIdentity = "stale-marker-identity"
	if err := e.Store.SeedBinding(ctx, markerIdentity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	personID := e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Drift Person', 'manual', 'human:x')`)
	if _, err := e.Owner.Exec(ctx, `
		INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
		VALUES ($1, 'person', $2, 0, 'stale-hash', $3, '[1,2,3]'::vector)`,
		e.WS, personID, staleRowIdentity); err != nil {
		t.Fatalf("seeding the stale-identity row: %v", err)
	}

	// The job's own args identity does NOT match what embedder actually
	// reports — the drift the guard exists to catch.
	err := e.Store.ReembedWorkspace(ctx, search.ReembedPass{Run: ids.NewV7(), Identity: "some-other-target-identity"}, ids.From[ids.WorkspaceKind](e.WS), embedder)
	if !errors.Is(err, search.ErrIdentityDrift) {
		t.Fatalf("ReembedWorkspace with a mismatched argsIdentity = %v, want ErrIdentityDrift", err)
	}

	if calls := len(fake.Calls()); calls != 0 {
		t.Fatalf("identity drift must not call the embedder, got %d calls", calls)
	}
	if got := e.storedEmbeddingModel(t, personID); got != staleRowIdentity {
		t.Fatalf("drift guard must not touch existing rows, model = %q, want unchanged %q", got, staleRowIdentity)
	}
	_, status, _, err := e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "idle" {
		t.Fatalf("identity drift must not alter the binding marker's status, got %q", status)
	}
}

// TestReembedRunMarkerIsReleasedByTheLastWorkspaceOutAndNotBefore pins the run
// lifecycle the fan-out turns on. Releasing on the FIRST workspace to finish
// would let a second reindex start on top of one still running; never releasing
// would wedge the marker at reembedding and refuse every later confirm. The
// removal is also idempotent, which is what makes a retried job harmless
// under at-least-once delivery.
func TestReembedRunMarkerIsReleasedByTheLastWorkspaceOutAndNotBefore(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const populated = "fake/populated@1024"
	const target = "fake/target@1024"
	if err := e.Store.SeedBinding(ctx, populated); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	second := SeedExtraWorkspace(t, e.Owner, "reembed-marker", false)

	run := claimOf(target)
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, run, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("ClaimAndEnqueueReembedding: %v", err)
	}
	fleet := []ids.WorkspaceID{ids.From[ids.WorkspaceKind](e.WS), ids.From[ids.WorkspaceKind](second)}
	if err := e.Store.SeedReembeddingFleet(ctx, run.Run, fleet, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("SeedReembeddingFleet: %v", err)
	}

	if err := e.Store.FinishWorkspaceReembedding(ctx, run.Run, fleet[0]); err != nil {
		t.Fatalf("finishing the first workspace: %v", err)
	}
	got, status, _, err := e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "reembedding" || got != populated {
		t.Fatalf("marker = %q/%q after one of two workspaces finished, want reembedding/%q — a second reindex could start over the one still running", status, got, populated)
	}

	// Idempotent: the same workspace reporting twice (a retried job) must not
	// count as the second workspace and release early.
	if err := e.Store.FinishWorkspaceReembedding(ctx, run.Run, fleet[0]); err != nil {
		t.Fatalf("re-finishing the first workspace: %v", err)
	}
	_, status, _, err = e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "reembedding" {
		t.Fatalf("marker = %q after ONE workspace reported twice, want reembedding", status)
	}

	if err := e.Store.FinishWorkspaceReembedding(ctx, run.Run, fleet[1]); err != nil {
		t.Fatalf("finishing the last workspace: %v", err)
	}
	got, status, _, err = e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "idle" || got != target {
		t.Fatalf("marker = %q/%q after the last workspace finished, want idle/%q", status, got, target)
	}

	// A straggler from the released run must find nothing to act on rather
	// than re-releasing a marker a later run may hold by then.
	if err := e.Store.FinishWorkspaceReembedding(ctx, run.Run, fleet[0]); err != nil {
		t.Fatalf("a straggler of a released run must be a no-op, got: %v", err)
	}
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, claimOf(target), func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("the released marker must be claimable again: %v", err)
	}
}
