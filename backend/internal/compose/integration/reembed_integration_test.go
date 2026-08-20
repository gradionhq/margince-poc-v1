// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The resumable corpus re-embed routine (ADR-0068 design §5.6-swap v7,
// Task 10): Reembed re-embeds every live entity of the installation
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

// TestReembedReembedsAllLiveEntitiesAndIsResumable seeds 3 people
// under a stale identity, then proves a single Reembed call under
// a NEW identity re-embeds all 3 (their stored model becomes the new
// identity) and reads EntitiesPending == 0 afterward. A SECOND pass over
// the same identity must cost zero additional embed calls — the
// resumability property Task 6's skip-compare exists to provide.
func TestReembedReembedsAllLiveEntitiesAndIsResumable(t *testing.T) {
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
		id := e.SeedID(t, `INSERT INTO person (id, full_name, source, captured_by) VALUES ($1, $2, 'manual', 'human:x')`, name)
		if _, err := e.Store.UpsertEmbedding(e.Admin(), "person", id, name, staleEmbedder); err != nil {
			t.Fatalf("seeding the stale-identity baseline for %s: %v", name, err)
		}
		personIDs[i] = id
	}
	baselineCalls := len(fake.Calls())

	run := ids.NewV7()
	if err := e.Store.Reembed(ctx, search.ReembedPass{Run: run, Identity: newIdentity}, newEmbedder); err != nil {
		t.Fatalf("Reembed: %v", err)
	}

	for i, id := range personIDs {
		if got := e.storedEmbeddingModel(t, id); got != newIdentity {
			t.Fatalf("person[%d] model = %q, want %q (must have been re-embedded under the new identity)", i, got, newIdentity)
		}
	}

	firstPassCalls := len(fake.Calls()) - baselineCalls
	if firstPassCalls != len(names) {
		t.Fatalf("first Reembed made %d embed calls, want %d (one per live entity)", firstPassCalls, len(names))
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
	if err := e.Store.Reembed(ctx, search.ReembedPass{Run: run, Identity: newIdentity}, newEmbedder); err != nil {
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
	// The condition names the tenant the WRITE IS RUNNING AS, not the tenant on
	// the row. Phase B keyed embedding on (entity_type, entity_id, chunk_ix)
	// alone, so a row belongs to the installation and carries whichever tenant
	// happened to write it first — a NEW.workspace_id test would then fire or
	// not depending on which pass won the race, which is not what "this
	// tenant's writes cannot land" means. The GUC is set by the pass's own
	// WithWorkspaceTx, so this fires for every write that tenant attempts.
	//
	// CREATE TRIGGER takes no bind parameters, so the tenant is interpolated;
	// it is a UUID rendered by ids.UUID.String(), never caller text.
	if _, err := owner.Exec(ctx, `
		CREATE TRIGGER embedding_write_fault_trigger
		BEFORE INSERT OR UPDATE ON embedding
		FOR EACH ROW WHEN (current_setting('app.workspace_id', true) = '`+ws.String()+`')
		EXECUTE FUNCTION embedding_write_fault()`); err != nil {
		t.Fatalf("arming the fault-injection trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DROP TRIGGER embedding_write_fault_trigger ON embedding`); err != nil {
			t.Errorf("dropping the fault-injection trigger: %v", err)
		}
	})
}

// TestReembedReportsAWriteItCouldNotLand is what the per-tenant-cost case
// becomes once there is one pass.
//
// It used to assert that a write fault cost only the workspace that could not
// write — the characterization of a much older bug, where a fleet pass walked
// every tenant inside one row and RETURNED on the first failure, leaving every
// workspace behind it in the fleet order un-re-embedded with nothing recording
// it. That property has no subject now: ADR-0091 §8 phase D left one corpus, so
// there is no second tenant's pass to be spared.
//
// What survives is the half that mattered: a pass whose writes cannot land
// REPORTS it. A pass that swallowed the fault would leave the marker released,
// the identity stamped, and an index that was never rebuilt — the same silence
// under a different shape.
func TestReembedReportsAWriteItCouldNotLand(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	embedder := fakeEmbedderNamed(t, fake, "model-isolation")
	identity, _ := embedder.EmbedIdentity()
	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	e.SeedID(t, `INSERT INTO person (id, full_name, source, captured_by) VALUES ($1, 'Unwritable Person', 'manual', 'human:x')`)
	failEmbeddingWritesFor(t, e.Owner, e.WS)

	if err := e.Store.Reembed(ctx, search.ReembedPass{Run: ids.NewV7(), Identity: identity}, embedder); err == nil {
		t.Fatal("a pass whose embedding writes could not land reported success — nothing records that the corpus was never rebuilt")
	}
}

// TestReembedIdentityDriftCancelsWithoutTouchingRows proves the
// entry guard fires — and touches NOTHING — when the embedder compose
// actually injected no longer agrees with the job's own target identity:
// an operator swapped the live embed binding after this job was
// enqueued. The worker maps ErrIdentityDrift to river.JobCancel so a stale
// job cancels cleanly instead of burning its ladder against an identity
// nothing serves anymore.
func TestReembedIdentityDriftCancelsWithoutTouchingRows(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	embedder := fakeEmbedderNamed(t, fake, "model-current")

	const markerIdentity = "stale-marker-identity"
	const staleRowIdentity = "stale-marker-identity"
	if err := e.Store.SeedBinding(ctx, markerIdentity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	personID := e.SeedID(t, `INSERT INTO person (id, full_name, source, captured_by) VALUES ($1, 'Drift Person', 'manual', 'human:x')`)
	if _, err := e.Owner.Exec(ctx, `
		INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
		VALUES ($1, 'person', $2, 0, 'stale-hash', $3, '[1,2,3]'::vector)`,
		e.WS, personID, staleRowIdentity); err != nil {
		t.Fatalf("seeding the stale-identity row: %v", err)
	}

	// The job's own args identity does NOT match what embedder actually
	// reports — the drift the guard exists to catch.
	err := e.Store.Reembed(ctx, search.ReembedPass{Run: ids.NewV7(), Identity: "some-other-target-identity"}, embedder)
	if !errors.Is(err, search.ErrIdentityDrift) {
		t.Fatalf("Reembed with a mismatched argsIdentity = %v, want ErrIdentityDrift", err)
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

// TestReembedRunMarkerIsHeldUntilTheRunEnds replaces a case about the pending
// set, which no longer exists.
//
// That case proved the marker released on the LAST workspace out and not the
// first: releasing early would let a second reindex start over one still
// running, and never releasing would refuse every later confirm. With one pass
// there is no "last one out" — but both failures it guarded are still failures,
// so what is left is asserted directly: held while the run is in flight, back
// when it ends, and idempotent so a retried job is harmless.
func TestReembedRunMarkerIsHeldUntilTheRunEnds(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const populated = "fake/populated@1024"
	const target = "fake/target@1024"
	if err := e.Store.SeedBinding(ctx, populated); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	run := claimOf(target)
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, run, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("ClaimAndEnqueueReembedding: %v", err)
	}
	got, status, _, err := e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "reembedding" || got != populated {
		t.Fatalf("marker = %q/%q while the run is in flight, want reembedding/%q — a second reindex could start over one still running", status, got, populated)
	}

	if err := e.Store.ReleaseReembedding(ctx, run.Run); err != nil {
		t.Fatalf("releasing the run: %v", err)
	}
	// Idempotent: a retried job reporting twice must not disturb the marker,
	// which after the collapse is the only at-least-once hazard left here.
	if err := e.Store.ReleaseReembedding(ctx, run.Run); err != nil {
		t.Fatalf("re-releasing the run: %v", err)
	}
	_, status, _, err = e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status == "reembedding" {
		t.Fatal("the marker is still held after the run ended — every later confirm is refused until a forced steal")
	}
}
