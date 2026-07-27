// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A block committed AFTER the sweep has already chosen its candidates must
// still stop the mapping from landing. SeedUserMap reads candidates in one
// transaction (usersMatchingEmail) and writes each mapping in a separate one
// (UpsertUserMap), so a guard placed on the READ cannot see a block committed
// in between — the mapping lands anyway and the user ends up mapped AND
// blocked, permanently, because the next sweep skips them and revalidation
// keeps the row whose email still matches. The guard therefore has to be in
// the upsert statement itself, which is what this test pins.
func TestBlockCommittedAfterCandidateSelectionStillStopsTheMapping(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-1": "rep@acme.test"})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	// Stand in for the sweep's candidate read: confirm the user IS a
	// candidate before any block exists.
	candidates, err := store.usersMatchingEmail(ctx, "rep@acme.test", "hubspot")
	if err != nil {
		t.Fatalf("selecting candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("want the user to be a seedable candidate, got %d candidates", len(candidates))
	}

	// The admin's unmap commits in the window between the sweep's read and
	// its write.
	seedAutoMapBlock(ctx, t, pool, rep, "hubspot")

	// The sweep's already-decided write now arrives.
	if err := store.UpsertUserMap(ctx, rep, "hubspot", "owner-1", "email"); err != nil {
		t.Fatalf("the sweep's upsert must be a quiet no-op for a blocked user, got: %v", err)
	}

	var mapped int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM mirror_user_map WHERE app_user_id = $1`, rep).Scan(&mapped)
	}); err != nil {
		t.Fatalf("counting mappings: %v", err)
	}
	if mapped != 0 {
		t.Fatalf("a blocked user must have no mapping row, got %d", mapped)
	}
}

// The block is not a wall against a human: match_source="manual" is the
// admin escape hatch (design.md §4.6 rule 4), so mapping a blocked user by
// hand must succeed AND clear the block, so the user is never left mapped
// and blocked at once.
func TestManualMapClearsTheBlockAndWrites(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-1": "rep@acme.test"})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	seedAutoMapBlock(ctx, t, pool, rep, "hubspot")
	if err := store.UpsertUserMap(ctx, rep, "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("a manual map must override a block: %v", err)
	}

	var mapped, blocked int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM mirror_user_map WHERE app_user_id = $1 AND match_source = 'manual'`,
			rep).Scan(&mapped); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM mirror_user_automap_block WHERE app_user_id = $1`, rep).Scan(&blocked)
	}); err != nil {
		t.Fatalf("reading state: %v", err)
	}
	if mapped != 1 {
		t.Fatalf("want one manual mapping row, got %d", mapped)
	}
	if blocked != 0 {
		t.Fatalf("a manual map must clear the block, %d block rows remain", blocked)
	}
}
