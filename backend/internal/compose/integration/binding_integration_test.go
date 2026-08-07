// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The deployment binding-marker store (ADR-0068 design §5.6-swap v7):
// SeedBinding's no-first-boot-wart property, the DERIVED reindex-needed
// signal (never a stored flag), the one-tx CAS+enqueue claim, and the
// per-workspace pending/token-sum rollups the advisory cost preview prices.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestSeedBindingOnEmptyStoreHasNoFirstBootWart(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const identity = "fake/seed@1024"

	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	populated, status, _, err := e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != identity {
		t.Fatalf("populated_identity = %q, want %q (seeding must plant the live config, not a sentinel)", populated, identity)
	}
	if status != "idle" {
		t.Fatalf("status = %q, want idle", status)
	}

	needed, err := e.Store.ReindexNeeded(ctx, identity)
	if err != nil {
		t.Fatalf("ReindexNeeded: %v", err)
	}
	if needed {
		t.Fatal("a freshly seeded, empty store must not read reindex-needed (first-boot wart)")
	}
}

func TestReindexNeededAfterStaleIdentityRow(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const identity = "fake/current@1024"

	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Stale Row Person', 'manual', 'human:x')`)
	// A row stamped under a DIFFERENT identity than currentIdentity — the
	// entity has an embedding row, just not a current one, so it must
	// still count as pending (the swap case, distinct from "no row at all").
	if _, err := e.Owner.Exec(ctx, `
		INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
		VALUES ($1, 'person', $2, 0, 'stale-hash', 'fake/old@1024', '[1,2,3]'::vector)`,
		e.WS, personID); err != nil {
		t.Fatalf("seeding the stale-identity row: %v", err)
	}

	pending, err := e.Store.EntitiesPending(ctx, identity)
	if err != nil {
		t.Fatalf("EntitiesPending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("EntitiesPending = %d, want 1 (the stale-identity row must count as pending)", pending)
	}

	needed, err := e.Store.ReindexNeeded(ctx, identity)
	if err != nil {
		t.Fatalf("ReindexNeeded: %v", err)
	}
	if !needed {
		t.Fatal("a stale-identity row must read reindex-needed")
	}
}

func TestSeedBindingIsIdempotentAndConcurrentSafe(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const first = "fake/first@1024"
	const second = "fake/second@1024"

	if err := e.Store.SeedBinding(ctx, first); err != nil {
		t.Fatalf("first SeedBinding: %v", err)
	}
	if err := e.Store.SeedBinding(ctx, second); err != nil {
		t.Fatalf("second SeedBinding must no-op, not error: %v", err)
	}
	populated, _, _, err := e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != first {
		t.Fatalf("populated_identity = %q, want %q (the second seed must not have overwritten the first)", populated, first)
	}

	// Two concurrent seeds against a fresh (unseeded) store both succeed —
	// ON CONFLICT DO NOTHING arbitrates the race inside Postgres, not here.
	e2 := SetupSearch(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = e2.Store.SeedBinding(context.Background(), "fake/concurrent@1024")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent SeedBinding[%d]: %v", i, err)
		}
	}
}

// TestClaimAndEnqueueReembeddingIsSingleFlightOnTheRun proves what makes the
// marker busy. The claim's dispatcher completes as soon as it has fanned the
// fleet out, so "some job of this kind is still active" stops being true long
// before the run is over: the run holding the marker is what has to refuse the
// second claim, and it is refused by name (ErrReembeddingInFlight) rather than
// by a bare zero row count that could equally mean an unseeded marker.
func TestClaimAndEnqueueReembeddingIsSingleFlightOnTheRun(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const identity = "fake/claim@1024"
	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	var ran bool
	err := e.Store.ClaimAndEnqueueReembedding(ctx, claimOf(identity), func(tx pgx.Tx) error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("ClaimAndEnqueueReembedding from idle: %v", err)
	}
	if !ran {
		t.Fatal("the enqueue callback must run inside the claim transaction")
	}
	_, status, _, err := e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "reembedding" {
		t.Fatalf("status = %q, want reembedding after a successful claim", status)
	}

	ran = false
	err = e.Store.ClaimAndEnqueueReembedding(ctx, claimOf(identity), func(tx pgx.Tx) error {
		ran = true
		return nil
	})
	if !errors.Is(err, search.ErrReembeddingInFlight) {
		t.Fatalf("second claim = %v, want ErrReembeddingInFlight — two runs would fan out over each other's pending set", err)
	}
	if ran {
		t.Fatal("a refused claim must not enqueue a second run's dispatcher")
	}
}

// TestClaimAndEnqueueReembeddingNamesAnUnseededMarker is the other half of the
// refusal: an unseeded marker is a deployment that skipped boot, not a run
// holding the marker, and the two answer different status codes — so they must
// not collapse into one error.
//
// Its own test because SetupSearch RESETS the database. Asserting this inside
// the single-flight test would mean tearing down the very claim that test is
// about, and would hold only for as long as it stayed the last assertion in the
// function.
func TestClaimAndEnqueueReembeddingNamesAnUnseededMarker(t *testing.T) {
	e := SetupSearch(t)
	err := e.Store.ClaimAndEnqueueReembedding(context.Background(),
		claimOf("fake/unseeded@1024"), func(pgx.Tx) error { return nil })
	if !errors.Is(err, search.ErrBindingNotSeeded) {
		t.Fatalf("claim against an unseeded marker = %v, want ErrBindingNotSeeded", err)
	}
}

func TestClaimAndEnqueueReembeddingRollsBackCASOnEnqueueError(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const identity = "fake/rollback@1024"
	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	enqueueErr := errors.New("enqueue exploded")
	err := e.Store.ClaimAndEnqueueReembedding(ctx, claimOf(identity), func(tx pgx.Tx) error {
		return enqueueErr
	})
	if !errors.Is(err, enqueueErr) {
		t.Fatalf("ClaimAndEnqueueReembedding error = %v, want %v", err, enqueueErr)
	}

	// The CAS must have rolled back with the failed callback — status
	// stays idle, never left stranded in reembedding with no live job.
	_, status, _, err := e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "idle" {
		t.Fatalf("status = %q, want idle (the CAS must roll back when the enqueue callback errors)", status)
	}
}

// TestReleaseReembeddingOnlyEverActsOnItsOwnRun proves the release is fenced on
// the run that claimed the marker: a release quoting some other run — a job row
// left over from a run that already ended — must not hand away a marker the
// current run is still holding, and must not stamp populated_identity on behalf
// of a run that never held it.
//
// What it does NOT pin, because the code does not give it: that the identity
// stamped is one the fleet was actually re-embedded under. A run releases when
// its last workspace has no outcome left to reach, exhausted attempts included,
// so populated_identity means "last released under" (search's
// releaseReembeddingTx).
func TestReleaseReembeddingOnlyEverActsOnItsOwnRun(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const original = "fake/orig@1024"
	const claimed = "fake/claimed@1024"
	if err := e.Store.SeedBinding(ctx, original); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	// Releasing while no run holds the marker must be a no-op: nothing was
	// claimed, so nothing should read populated under a never-run job's identity.
	if err := e.Store.ReleaseReembedding(ctx, ids.NewV7()); err != nil {
		t.Fatalf("ReleaseReembedding with no run in flight: %v", err)
	}
	populated, status, _, err := e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != original || status != "idle" {
		t.Fatalf("releasing an unclaimed marker must no-op, got populated=%q status=%q", populated, status)
	}

	run := ids.NewV7()
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, search.ReembedClaim{Run: run, TargetIdentity: claimed}, func(tx pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("ClaimAndEnqueueReembedding: %v", err)
	}
	if err := e.Store.ReleaseReembedding(ctx, ids.NewV7()); err != nil {
		t.Fatalf("ReleaseReembedding under a run that does not hold the marker: %v", err)
	}
	populated, status, _, err = e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != original || status != "reembedding" {
		t.Fatalf("a run that does not hold the marker released it: populated=%q status=%q", populated, status)
	}

	if err := e.Store.ReleaseReembedding(ctx, run); err != nil {
		t.Fatalf("ReleaseReembedding from the claiming run: %v", err)
	}
	populated, status, _, err = e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != claimed {
		t.Fatalf("populated_identity = %q, want %q", populated, claimed)
	}
	if status != "idle" {
		t.Fatalf("status = %q, want idle after the run released", status)
	}
}

func TestReindexNeededOnDimsOnlyDifference(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	// Same provider/model, different width — a real operator scenario
	// (widening the embed dimension), not a different model at all.
	const populated = "gemini/embed-001@1024"
	const configured = "gemini/embed-001@768"

	if err := e.Store.SeedBinding(ctx, populated); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	needed, err := e.Store.ReindexNeeded(ctx, configured)
	if err != nil {
		t.Fatalf("ReindexNeeded: %v", err)
	}
	if !needed {
		t.Fatal("a dims-only identity difference must read reindex-needed")
	}
}

func TestPendingAndTokenSumAggregateAcrossWorkspaces(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const identity = "fake/agg@1024"
	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	// A sibling workspace search's own setup does not create — proves the
	// fleet enumeration (not just the harness's one workspace) is real.
	ws2 := ids.NewV7()
	if _, err := e.Owner.Exec(ctx, `INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Search Two', 'search-two', 'EUR')`, ws2); err != nil {
		t.Fatalf("seeding the sibling workspace: %v", err)
	}

	const nameOne = "Pending One"
	const nameOrg = "Pending Org"
	const nameTwo = "Pending Two"

	e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, '`+nameOne+`', 'manual', 'human:x')`)
	e.seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by) VALUES ($1, $2, '`+nameOrg+`', 'manual', 'human:x')`)
	// A lead with every text-bearing column NULL: concat_ws collapses to
	// '', so it must NOT count as pending — the non-empty qualifier.
	e.seed(t, `INSERT INTO lead (id, workspace_id, source, captured_by) VALUES ($1, $2, 'manual', 'human:x')`)
	// Already covered at the current identity: must not count as pending.
	coveredID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Already Covered', 'manual', 'human:x')`)
	if _, err := e.Owner.Exec(ctx, `
		INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
		VALUES ($1, 'person', $2, 0, 'covered-hash', $3, '[1,2,3]'::vector)`,
		e.WS, coveredID, identity); err != nil {
		t.Fatalf("seeding the already-covered row: %v", err)
	}

	ws2PersonID := ids.NewV7()
	if _, err := e.Owner.Exec(ctx, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, $3, 'manual', 'human:x')`,
		ws2PersonID, ws2, nameTwo); err != nil {
		t.Fatalf("seeding the sibling workspace's person: %v", err)
	}

	counts, err := e.Store.PendingByWorkspace(ctx, identity)
	if err != nil {
		t.Fatalf("PendingByWorkspace: %v", err)
	}
	tokens, err := e.Store.TokenSumByWorkspace(ctx, identity)
	if err != nil {
		t.Fatalf("TokenSumByWorkspace: %v", err)
	}
	total, err := e.Store.EntitiesPending(ctx, identity)
	if err != nil {
		t.Fatalf("EntitiesPending: %v", err)
	}

	wsKey := ids.From[ids.WorkspaceKind](e.WS)
	ws2Key := ids.From[ids.WorkspaceKind](ws2)

	if counts[wsKey] != 2 {
		t.Fatalf("counts[e.WS] = %d, want 2 (person + organization; the null lead and the already-covered person must be excluded)", counts[wsKey])
	}
	if counts[ws2Key] != 1 {
		t.Fatalf("counts[ws2] = %d, want 1", counts[ws2Key])
	}

	sum := 0
	for _, c := range counts {
		sum += c
	}
	if sum != total {
		t.Fatalf("sum of PendingByWorkspace = %d, EntitiesPending = %d — must agree", sum, total)
	}
	if total != 3 {
		t.Fatalf("EntitiesPending = %d, want 3", total)
	}

	wantWSTokens := int64((len(nameOne) + len(nameOrg)) / 4)
	if tokens[wsKey] != wantWSTokens {
		t.Fatalf("tokens[e.WS] = %d, want %d (SUM(length)/4 over the pending set)", tokens[wsKey], wantWSTokens)
	}
	wantWS2Tokens := int64(len(nameTwo) / 4)
	if tokens[ws2Key] != wantWS2Tokens {
		t.Fatalf("tokens[ws2] = %d, want %d", tokens[ws2Key], wantWS2Tokens)
	}
}
