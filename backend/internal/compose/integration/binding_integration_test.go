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

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestSeedBindingOnEmptyStoreHasNoFirstBootWart(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()
	const identity = "fake/seed@1024"

	if err := e.store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	populated, status, _, err := e.store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != identity {
		t.Fatalf("populated_identity = %q, want %q (seeding must plant the live config, not a sentinel)", populated, identity)
	}
	if status != "idle" {
		t.Fatalf("status = %q, want idle", status)
	}

	needed, err := e.store.ReindexNeeded(ctx, identity)
	if err != nil {
		t.Fatalf("ReindexNeeded: %v", err)
	}
	if needed {
		t.Fatal("a freshly seeded, empty store must not read reindex-needed (first-boot wart)")
	}
}

func TestReindexNeededAfterStaleIdentityRow(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()
	const identity = "fake/current@1024"

	if err := e.store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Stale Row Person', 'manual', 'human:x')`)
	// A row stamped under a DIFFERENT identity than currentIdentity — the
	// entity has an embedding row, just not a current one, so it must
	// still count as pending (the swap case, distinct from "no row at all").
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
		VALUES ($1, 'person', $2, 0, 'stale-hash', 'fake/old@1024', '[1,2,3]'::vector)`,
		e.WS, personID); err != nil {
		t.Fatalf("seeding the stale-identity row: %v", err)
	}

	pending, err := e.store.EntitiesPending(ctx, identity)
	if err != nil {
		t.Fatalf("EntitiesPending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("EntitiesPending = %d, want 1 (the stale-identity row must count as pending)", pending)
	}

	needed, err := e.store.ReindexNeeded(ctx, identity)
	if err != nil {
		t.Fatalf("ReindexNeeded: %v", err)
	}
	if !needed {
		t.Fatal("a stale-identity row must read reindex-needed")
	}
}

func TestSeedBindingIsIdempotentAndConcurrentSafe(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()
	const first = "fake/first@1024"
	const second = "fake/second@1024"

	if err := e.store.SeedBinding(ctx, first); err != nil {
		t.Fatalf("first SeedBinding: %v", err)
	}
	if err := e.store.SeedBinding(ctx, second); err != nil {
		t.Fatalf("second SeedBinding must no-op, not error: %v", err)
	}
	populated, _, _, err := e.store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != first {
		t.Fatalf("populated_identity = %q, want %q (the second seed must not have overwritten the first)", populated, first)
	}

	// Two concurrent seeds against a fresh (unseeded) store both succeed —
	// ON CONFLICT DO NOTHING arbitrates the race inside Postgres, not here.
	e2 := setupSearch(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = e2.store.SeedBinding(context.Background(), "fake/concurrent@1024")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent SeedBinding[%d]: %v", i, err)
		}
	}
}

func TestClaimAndEnqueueReembeddingRunsCallbackInOneTx(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()
	const identity = "fake/claim@1024"
	if err := e.store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	var ran bool
	err := e.store.ClaimAndEnqueueReembedding(ctx, func(tx pgx.Tx) error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("ClaimAndEnqueueReembedding from idle: %v", err)
	}
	if !ran {
		t.Fatal("the enqueue callback must run inside the claim transaction")
	}
	_, status, _, err := e.store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "reembedding" {
		t.Fatalf("status = %q, want reembedding after a successful claim", status)
	}

	// From reembedding: recovery — the CAS still moves (a no-op status
	// value) and the callback still runs, e.g. a discarded job re-confirm.
	ran = false
	err = e.store.ClaimAndEnqueueReembedding(ctx, func(tx pgx.Tx) error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("ClaimAndEnqueueReembedding from reembedding: %v", err)
	}
	if !ran {
		t.Fatal("the callback must still run when recovering a stuck reembedding row")
	}
	_, status, _, err = e.store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "reembedding" {
		t.Fatalf("status = %q, want reembedding to persist across a recovery claim", status)
	}
}

func TestClaimAndEnqueueReembeddingRollsBackCASOnEnqueueError(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()
	const identity = "fake/rollback@1024"
	if err := e.store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	enqueueErr := errors.New("enqueue exploded")
	err := e.store.ClaimAndEnqueueReembedding(ctx, func(tx pgx.Tx) error {
		return enqueueErr
	})
	if !errors.Is(err, enqueueErr) {
		t.Fatalf("ClaimAndEnqueueReembedding error = %v, want %v", err, enqueueErr)
	}

	// The CAS must have rolled back with the failed callback — status
	// stays idle, never left stranded in reembedding with no live job.
	_, status, _, err := e.store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "idle" {
		t.Fatalf("status = %q, want idle (the CAS must roll back when the enqueue callback errors)", status)
	}
}

func TestCompleteReembeddingOnlyFromReembeddingStatus(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()
	const original = "fake/orig@1024"
	const completed = "fake/completed@1024"
	if err := e.store.SeedBinding(ctx, original); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	// Calling complete while idle must be a no-op: nothing was claimed, so
	// nothing should be marked populated under a never-run job's identity.
	if err := e.store.CompleteReembedding(ctx, completed); err != nil {
		t.Fatalf("CompleteReembedding from idle: %v", err)
	}
	populated, status, _, err := e.store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != original || status != "idle" {
		t.Fatalf("CompleteReembedding from idle must no-op, got populated=%q status=%q", populated, status)
	}

	if err := e.store.ClaimAndEnqueueReembedding(ctx, func(tx pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("ClaimAndEnqueueReembedding: %v", err)
	}
	if err := e.store.CompleteReembedding(ctx, completed); err != nil {
		t.Fatalf("CompleteReembedding from reembedding: %v", err)
	}
	populated, status, _, err = e.store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != completed {
		t.Fatalf("populated_identity = %q, want %q", populated, completed)
	}
	if status != "idle" {
		t.Fatalf("status = %q, want idle after completion", status)
	}
}

func TestReindexNeededOnDimsOnlyDifference(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()
	// Same provider/model, different width — a real operator scenario
	// (widening the embed dimension), not a different model at all.
	const populated = "gemini/embed-001@1024"
	const configured = "gemini/embed-001@768"

	if err := e.store.SeedBinding(ctx, populated); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	needed, err := e.store.ReindexNeeded(ctx, configured)
	if err != nil {
		t.Fatalf("ReindexNeeded: %v", err)
	}
	if !needed {
		t.Fatal("a dims-only identity difference must read reindex-needed")
	}
}

func TestPendingAndTokenSumAggregateAcrossWorkspaces(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()
	const identity = "fake/agg@1024"
	if err := e.store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}

	// A sibling workspace search's own setup does not create — proves the
	// fleet enumeration (not just the harness's one workspace) is real.
	ws2 := ids.NewV7()
	if _, err := e.owner.Exec(ctx, `INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Search Two', 'search-two', 'EUR')`, ws2); err != nil {
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
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
		VALUES ($1, 'person', $2, 0, 'covered-hash', $3, '[1,2,3]'::vector)`,
		e.WS, coveredID, identity); err != nil {
		t.Fatalf("seeding the already-covered row: %v", err)
	}

	ws2PersonID := ids.NewV7()
	if _, err := e.owner.Exec(ctx, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, $3, 'manual', 'human:x')`,
		ws2PersonID, ws2, nameTwo); err != nil {
		t.Fatalf("seeding the sibling workspace's person: %v", err)
	}

	counts, err := e.store.PendingByWorkspace(ctx, identity)
	if err != nil {
		t.Fatalf("PendingByWorkspace: %v", err)
	}
	tokens, err := e.store.TokenSumByWorkspace(ctx, identity)
	if err != nil {
		t.Fatalf("TokenSumByWorkspace: %v", err)
	}
	total, err := e.store.EntitiesPending(ctx, identity)
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
