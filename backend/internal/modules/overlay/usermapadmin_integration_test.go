// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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

// Deleting the map row alone leaves the mirror_visibility grants dangling, so
// the user keeps reading records they were just unmapped from. The revoke has
// to run through recomputeForOwnerTx.
func TestBlockAutoMapRevokesTheVisibilityGrants(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-1": "rep@acme.test"})
	repCtx, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	if err := store.UpsertUserMap(ctx, rep, "hubspot", "owner-1", "email"); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := store.Ingest(ctx, Record{
		ObjectClass: "contact", ExternalID: "hs-1",
		Fields:          map[string]any{"firstname": "Mapped"},
		ModifiedAt:      time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the owned record: %v", err)
	}

	if _, err := store.Get(repCtx, "contact", "hs-1"); err != nil {
		t.Fatalf("the mapped user should see the record before the unmap: %v", err)
	}

	if err := store.BlockAutoMap(ctx, rep, "hubspot"); err != nil {
		t.Fatalf("blocking auto-map: %v", err)
	}

	if _, err := store.Get(repCtx, "contact", "hs-1"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("after the unmap the record must be invisible (ErrNotFound), got: %v", err)
	}
	if got := countMappingAudits(ctx, t, pool, "archive"); got != 1 {
		t.Fatalf("want 1 archive audit for the admin unmap, got %d", got)
	}
}

// DELETE is idempotent: unmapping an already-unmapped user still records the
// admin's decision, so a retry or a double-click is not an error.
func TestBlockAutoMapOnAnUnmappedUserIsIdempotent(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-1": "rep@acme.test"})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	for range 2 {
		if err := store.BlockAutoMap(ctx, rep, "hubspot"); err != nil {
			t.Fatalf("blocking an unmapped user must succeed: %v", err)
		}
	}
	var blocked int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM mirror_user_automap_block WHERE app_user_id = $1`, rep).Scan(&blocked)
	}); err != nil {
		t.Fatalf("counting blocks: %v", err)
	}
	if blocked != 1 {
		t.Fatalf("want exactly 1 block row after two calls, got %d", blocked)
	}
}

// The list is the UI's whole data source: it must include users with NO
// mapping (they are the ones an admin has to act on) and mark blocked ones.
func TestListUserMapIncludesUnmappedAndBlockedUsers(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-1": "mapped@acme.test"})
	_, mappedRaw := testWorkspaceCtxAsUser(t, ws, "mapped@acme.test")
	mapped := ids.From[ids.UserKind](mappedRaw)
	_, unmappedRaw := testWorkspaceCtxAsUser(t, ws, "unmapped@acme.test")
	unmapped := ids.From[ids.UserKind](unmappedRaw)
	_, blockedRaw := testWorkspaceCtxAsUser(t, ws, "blocked@acme.test")
	blocked := ids.From[ids.UserKind](blockedRaw)

	if err := store.UpsertUserMap(ctx, mapped, "hubspot", "owner-1", "email"); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := store.BlockAutoMap(ctx, blocked, "hubspot"); err != nil {
		t.Fatalf("blocking: %v", err)
	}

	entries, _, err := store.ListUserMap(ctx, "hubspot", "", 50)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	byID := map[ids.UserID]UserMapEntry{}
	for _, e := range entries {
		byID[e.AppUserID] = e
	}
	if got := byID[mapped].IncumbentUserID; got != "owner-1" {
		t.Fatalf("the mapped user should report owner-1, got %q", got)
	}
	if got := byID[unmapped].IncumbentUserID; got != "" {
		t.Fatalf("the unmapped user should report no owner, got %q", got)
	}
	if _, present := byID[unmapped]; !present {
		t.Fatal("an unmapped user must still appear in the list — they are the ones needing action")
	}
	if !byID[blocked].Blocked {
		t.Fatal("the blocked user must be flagged as blocked")
	}
}

// A passport identity has no incumbent counterpart, and an archived seat no
// longer logs in — offering either a mapping affordance invites an admin to
// grant mirror visibility to something that should not have it.
func TestListUserMapExcludesAgentAndArchivedUsers(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})
	_, humanRaw := testWorkspaceCtxAsUser(t, ws, "human@acme.test")
	human := ids.From[ids.UserKind](humanRaw)
	agent := seedAgentUser(t, ws, "agent@acme.test")
	archived := seedArchivedUser(t, ws, "gone@acme.test")

	entries, _, err := store.ListUserMap(ctx, "hubspot", "", 50)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	present := map[ids.UserID]bool{}
	for _, e := range entries {
		present[e.AppUserID] = true
	}
	if !present[human] {
		t.Fatal("a human user must be listed")
	}
	if present[agent] {
		t.Fatal("an agent user must not be offered a mapping")
	}
	if present[archived] {
		t.Fatal("an archived user must not be offered a mapping")
	}
}

// The composite FK, not RLS, is what must reject this: RLS would merely hide
// the other workspace's user, leaving a block row that references nothing.
func TestBlockAutoMapCannotTargetAnotherWorkspacesUser(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})
	foreign := seedUserInOtherWorkspace(t, "elsewhere@other.test")

	if err := store.BlockAutoMap(ctx, foreign, "hubspot"); err == nil {
		t.Fatal("blocking a user from another workspace must be rejected by the database")
	}
}
