// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// The fail-closed visibility deny-join (design.md §4.6, OA-T07/OA-T08)
// over a real, migrated Postgres. Four cases, driven end-to-end through
// Ingest (proving the visibility seam is filled, not bypassed) and Get/List
// (proving the deny-join itself):
//
//   - a mapped user whose mirror_visibility row says can_see=true sees
//     the record.
//   - an UNMAPPED user (zero mirror_user_map rows at all) gets
//     apperrors.ErrNotFound — existence-hiding, never a 403.
//   - a MAPPED user with no mirror_visibility entry for this particular
//     record (they are mapped, just not to this record's owner) is
//     hidden exactly like the unmapped case, from the caller's view.
//   - a NULL-owner record is fail-closed hidden for everyone, including a
//     user who is validly mapped to some other owner. §4.6 leaves the
//     null-owner rule with a fallback of "workspace-visible only if
//     HubSpot's own default makes it so, else hidden" — this build has no
//     read on HubSpot's own default-sharing setting, so it takes the
//     "else hidden" half rather than guess a leak (see the comment on
//     ProjectOwnerVisibility in visibility.go for the full citation).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestVisibilityDenyJoinFourCases(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})

	actorA, ok := principal.Actor(ctx)
	if !ok {
		t.Fatal("testWorkspaceCtx did not bind an actor")
	}
	userA := ids.From[ids.UserKind](actorA.UserID)

	ctxUnmapped, _ := testWorkspaceCtxAsUser(t, ws, "unmapped@overlay.test")
	ctxOtherOwner, otherOwnerRaw := testWorkspaceCtxAsUser(t, ws, "other-owner@overlay.test")
	userOtherOwner := ids.From[ids.UserKind](otherOwnerRaw)

	const objectClass = "contact"
	const ownedExternalID = "ext-owned"
	const nullOwnerExternalID = "ext-null-owner"

	// userA maps to owner-1 (the record's owner); userOtherOwner maps to
	// an unrelated owner-3 — mapped, but never granted visibility into
	// owner-1's record.
	if err := store.UpsertUserMap(ctx, userA, "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping userA to owner-1: %v", err)
	}
	if err := store.UpsertUserMap(ctx, userOtherOwner, "hubspot", "owner-3", "manual"); err != nil {
		t.Fatalf("mapping userOtherOwner to owner-3: %v", err)
	}

	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: ownedExternalID,
		Fields:          map[string]any{"firstname": "Owned"},
		ModifiedAt:      time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the owned record: %v", err)
	}
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: nullOwnerExternalID,
		Fields:     map[string]any{"firstname": "Unowned"},
		ModifiedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		// OwnerExternalID left "" — the null-owner case.
	}); err != nil {
		t.Fatalf("ingesting the null-owner record: %v", err)
	}

	t.Run("mapped user with can_see=true sees the row", func(t *testing.T) {
		row, err := store.Get(ctx, objectClass, ownedExternalID)
		if err != nil {
			t.Fatalf("expected the owner's mapped user to see the record, got: %v", err)
		}
		if row.Fields["firstname"] != "Owned" {
			t.Fatalf("wrong row returned: %+v", row)
		}
	})

	t.Run("unmapped user is fail-closed hidden (ErrNotFound, not 403)", func(t *testing.T) {
		_, err := store.Get(ctxUnmapped, objectClass, ownedExternalID)
		if !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("an unmapped user must answer ErrNotFound (existence-hiding), got: %v", err)
		}
	})

	t.Run("mapped user with no visibility entry for this record is hidden", func(t *testing.T) {
		_, err := store.Get(ctxOtherOwner, objectClass, ownedExternalID)
		if !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("a mapped user with no can_see row for this record must be hidden, got: %v", err)
		}
	})

	t.Run("null-owner record is fail-closed hidden for everyone", func(t *testing.T) {
		_, err := store.Get(ctx, objectClass, nullOwnerExternalID)
		if !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("a null-owner record must be hidden even from a validly-mapped user, got: %v", err)
		}
	})
}

// TestListAppliesTheSameDenyJoinAsGet proves List is not a second,
// unguarded read path — the brief requires the deny-join on BOTH Get and
// List, and a join added to only one of them would be exactly the
// un-gated-read window ADR-0044 forbids on whichever one was skipped.
func TestListAppliesTheSameDenyJoinAsGet(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})

	actor, _ := principal.Actor(ctx)
	userA := ids.From[ids.UserKind](actor.UserID)
	ctxUnmapped, _ := testWorkspaceCtxAsUser(t, ws, "unmapped-list@overlay.test")

	const objectClass = "contact"
	if err := store.UpsertUserMap(ctx, userA, "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping userA to owner-1: %v", err)
	}
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: "ext-visible",
		Fields: map[string]any{"firstname": "Visible"}, ModifiedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the visible record: %v", err)
	}
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: "ext-hidden",
		Fields: map[string]any{"firstname": "Hidden"}, ModifiedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		// no owner ⇒ hidden from everyone.
	}); err != nil {
		t.Fatalf("ingesting the hidden record: %v", err)
	}

	rows, _, err := store.List(ctx, objectClass, "", 50)
	if err != nil {
		t.Fatalf("listing as the mapped user: %v", err)
	}
	if len(rows) != 1 || rows[0].ExternalID != "ext-visible" {
		t.Fatalf("List must return only the visible record, got: %+v", rows)
	}

	if _, _, err := store.List(ctxUnmapped, objectClass, "", 50); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an unmapped user's List must answer ErrNotFound, got: %v", err)
	}
}

// TestRecomputeForOwnerUnhidesAlreadyIngestedRecords proves the recompute
// trigger design.md §4.6 requires: "the map is incomplete at backfill
// time; a row whose owner is unmapped then fails-closed (correct) but
// must be recomputed when the mapping row is later added." Without this,
// a record ingested before its owner is mapped stays hidden forever, with
// no branch-1 remedy short of a full bulk refresh.
func TestRecomputeForOwnerUnhidesAlreadyIngestedRecords(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})

	actor, _ := principal.Actor(ctx)
	userA := ids.From[ids.UserKind](actor.UserID)
	const objectClass = "contact"
	const externalID = "ext-late-mapped"

	// The record lands BEFORE its owner is mapped — fail-closed, correct.
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields: map[string]any{"firstname": "LateMapped"}, ModifiedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		OwnerExternalID: "owner-9",
	}); err != nil {
		t.Fatalf("ingesting the record: %v", err)
	}
	if _, err := store.Get(ctx, objectClass, externalID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("before the owner is mapped, the record must be hidden, got: %v", err)
	}

	// The mapping arrives later — UpsertUserMap must recompute, not just
	// take effect on the NEXT ingest.
	if err := store.UpsertUserMap(ctx, userA, "hubspot", "owner-9", "manual"); err != nil {
		t.Fatalf("mapping userA to owner-9: %v", err)
	}
	if _, err := store.Get(ctx, objectClass, externalID); err != nil {
		t.Fatalf("after the owner is mapped, the already-ingested record must be visible, got: %v", err)
	}
}

// TestUpsertUserMapAppliesThePinnedEmailRule proves design.md §4.6's
// pinned email-match rule end to end: a normalized (case/whitespace)
// match writes the row; a mismatch, or an incumbent owner this build
// cannot resolve at all, writes nothing (fail-closed, never a guessed
// match).
func TestUpsertUserMapAppliesThePinnedEmailRule(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-alice": "alice@example.com"})

	ctxAlice, aliceRaw := testWorkspaceCtxAsUser(t, ws, "  Alice@Example.com  ")
	alice := ids.From[ids.UserKind](aliceRaw)
	ctxBob, bobRaw := testWorkspaceCtxAsUser(t, ws, "bob@example.com")
	bob := ids.From[ids.UserKind](bobRaw)

	const objectClass = "contact"
	const externalID = "ext-alice-owned"
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields: map[string]any{"firstname": "AliceOwned"}, ModifiedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		OwnerExternalID: "owner-alice",
	}); err != nil {
		t.Fatalf("ingesting the record: %v", err)
	}

	t.Run("a normalized email match writes the row and grants visibility", func(t *testing.T) {
		if err := store.UpsertUserMap(ctx, alice, "hubspot", "owner-alice", "email"); err != nil {
			t.Fatalf("expected the case/whitespace-normalized email to match, got: %v", err)
		}
		if _, err := store.Get(ctxAlice, objectClass, externalID); err != nil {
			t.Fatalf("alice must now see the record she was matched to, got: %v", err)
		}
	})

	t.Run("a mismatched email writes no row (fail-closed)", func(t *testing.T) {
		if err := store.UpsertUserMap(ctx, bob, "hubspot", "owner-alice", "email"); err == nil {
			t.Fatal("expected the mismatched email to be rejected")
		}
		if _, err := store.Get(ctxBob, objectClass, externalID); !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("bob must still be unmapped/hidden after the rejected match, got: %v", err)
		}
	})

	t.Run("an unresolvable incumbent owner writes no row (fail-closed)", func(t *testing.T) {
		if err := store.UpsertUserMap(ctx, bob, "hubspot", "owner-unknown", "email"); err == nil {
			t.Fatal("expected an owner email resolution failure to be treated as a rejected match")
		}
	})
}

// TestIngestRevalidatesEmailSourcedMapOnOwnerChange proves design.md
// §4.6 rule 5: "the mapping is re-validated when the incumbent user's
// email changes … dropping to fail-closed until re-matched or manually
// overridden." Ingest never sees an email directly (Record carries only
// an owner id), so a record's owner_external_id changing to a
// previously-matched incumbent user is the trigger that re-verifies that
// user's email-sourced mapping is still correct.
func TestIngestRevalidatesEmailSourcedMapOnOwnerChange(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-new": "carol@example.com"})

	ctxDave, daveRaw := testWorkspaceCtxAsUser(t, ws, "dave@example.test")
	dave := ids.From[ids.UserKind](daveRaw)

	// Seed a STALE email-sourced mapping directly — simulating a mapping
	// that was correct when it was made, before "owner-new"'s email
	// changed to carol@example.com.
	if err := seedStaleEmailMap(ctx, pool, dave, "owner-new"); err != nil {
		t.Fatalf("seeding the stale mapping: %v", err)
	}

	const objectClass = "contact"
	const externalID = "ext-reassigned"
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields: map[string]any{"firstname": "First"}, ModifiedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		OwnerExternalID: "owner-old",
	}); err != nil {
		t.Fatalf("initial ingest: %v", err)
	}

	// The record is reassigned to "owner-new" — the trigger.
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields: map[string]any{"firstname": "Reassigned"}, ModifiedAt: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
		OwnerExternalID: "owner-new",
	}); err != nil {
		t.Fatalf("reassignment ingest: %v", err)
	}

	if _, err := store.Get(ctxDave, objectClass, externalID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("dave's stale email-sourced mapping must have been dropped, got: %v", err)
	}
}

// TestUpsertUserMapRemapRevokesTheOldOwnersRecords proves the fix for the
// remap-revocation gap: remapping appUser from one incumbent owner to
// another must, in the SAME call, revoke every can_see=true row the OLD
// mapping granted — not just add a new one. Reads are gated on
// mirror_visibility.can_see (never a live mirror_user_map join), so any
// stale can_see=true left behind by a remap is a silent retained-access
// bug. Covers the multi-record case (the old owner has more than one
// mirrored record) since a fix that only re-clears the FIRST record found
// would still leak.
func TestUpsertUserMapRemapRevokesTheOldOwnersRecords(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})

	actor, _ := principal.Actor(ctx)
	userA := ids.From[ids.UserKind](actor.UserID)
	const objectClass = "contact"
	const ownerXFirst = "ext-ownerx-1"
	const ownerXSecond = "ext-ownerx-2"
	const ownerYRecord = "ext-ownery-1"

	if err := store.UpsertUserMap(ctx, userA, "hubspot", "owner-x", "manual"); err != nil {
		t.Fatalf("mapping userA to owner-x: %v", err)
	}
	// owner-x's records are older than owner-y's — fixed, ordered
	// timestamps so the fixture is stable, not wall-clock dependent.
	ownerXModified := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	ownerYModified := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	for _, ext := range []string{ownerXFirst, ownerXSecond} {
		if err := store.Ingest(ctx, Record{
			ObjectClass: objectClass, ExternalID: ext,
			Fields: map[string]any{"firstname": "OwnerX"}, ModifiedAt: ownerXModified,
			OwnerExternalID: "owner-x",
		}); err != nil {
			t.Fatalf("ingesting %s owned by owner-x: %v", ext, err)
		}
	}
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: ownerYRecord,
		Fields: map[string]any{"firstname": "OwnerY"}, ModifiedAt: ownerYModified,
		OwnerExternalID: "owner-y",
	}); err != nil {
		t.Fatalf("ingesting the owner-y record: %v", err)
	}

	// Before the remap: userA sees both of owner-x's records, and neither
	// of owner-y's (not yet mapped to owner-y at all).
	for _, ext := range []string{ownerXFirst, ownerXSecond} {
		if _, err := store.Get(ctx, objectClass, ext); err != nil {
			t.Fatalf("before remap, userA must see owner-x's %s, got: %v", ext, err)
		}
	}
	if _, err := store.Get(ctx, objectClass, ownerYRecord); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("before remap, userA must not see owner-y's record, got: %v", err)
	}

	// Remap userA from owner-x to owner-y.
	if err := store.UpsertUserMap(ctx, userA, "hubspot", "owner-y", "manual"); err != nil {
		t.Fatalf("remapping userA to owner-y: %v", err)
	}

	// After the remap: userA must have LOST access to every one of
	// owner-x's records (the revocation this fix adds) and GAINED access
	// to owner-y's.
	for _, ext := range []string{ownerXFirst, ownerXSecond} {
		if _, err := store.Get(ctx, objectClass, ext); !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("after remap, userA must no longer see owner-x's %s, got: %v", ext, err)
		}
	}
	if _, err := store.Get(ctx, objectClass, ownerYRecord); err != nil {
		t.Fatalf("after remap, userA must see owner-y's record, got: %v", err)
	}
}

// TestRevalidateEmailMappingDeleteRevokesVisibility proves the fix for
// Finding 1's delete half: when revalidateEmailMapping drops a
// now-invalid email-sourced mirror_user_map row, the user's
// mirror_visibility rows for that owner's records must be revoked in the
// same pass — not just the mapping row deleted while the can_see=true
// projection it justified is left standing.
func TestRevalidateEmailMappingDeleteRevokesVisibility(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	emails := stubOwnerEmails{"owner-z": "dana@example.com"}
	store := NewMirrorStore(pool, emails)

	ctxDana, danaRaw := testWorkspaceCtxAsUser(t, ws, "dana@example.com")
	dana := ids.From[ids.UserKind](danaRaw)

	const objectClass = "contact"
	const externalID = "ext-ownerz-1"
	if err := store.UpsertUserMap(ctx, dana, "hubspot", "owner-z", "email"); err != nil {
		t.Fatalf("mapping dana to owner-z by email: %v", err)
	}
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields: map[string]any{"firstname": "OwnerZ"}, ModifiedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		OwnerExternalID: "owner-z",
	}); err != nil {
		t.Fatalf("ingesting the owner-z record: %v", err)
	}
	if _, err := store.Get(ctxDana, objectClass, externalID); err != nil {
		t.Fatalf("before revalidation, dana must see owner-z's record, got: %v", err)
	}

	// owner-z's email changes (dana's stored email no longer matches) —
	// drive revalidateEmailMapping directly the way both call sites do,
	// inside its own transaction.
	emails["owner-z"] = "someone-else@example.com"
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return store.revalidateEmailMapping(ctx, tx, emails, "owner-z")
	}); err != nil {
		t.Fatalf("revalidating owner-z's email mapping: %v", err)
	}

	if _, err := store.Get(ctxDana, objectClass, externalID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("after the mapping is invalidated, dana must no longer see owner-z's record, got: %v", err)
	}
}

// TestRevalidateEmailMappingsPeriodicSweepCatchesEmailChangeAlone proves
// design.md §4.6 rule 5's other half: an owner's email can change with NO
// record ever being reassigned, so Ingest's own reassignment-triggered
// revalidateEmailMapping call never runs for that owner. The exported
// RevalidateEmailMappings — the periodic sweep compose/jobs.go's
// reconcileConnection drives once per connection per tick — must catch
// this by re-checking every already-mapped, email-sourced owner
// regardless of any record ingest happening at all.
func TestRevalidateEmailMappingsPeriodicSweepCatchesEmailChangeAlone(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	emails := stubOwnerEmails{"owner-w": "erin@example.com"}
	store := NewMirrorStore(pool, emails)

	ctxErin, erinRaw := testWorkspaceCtxAsUser(t, ws, "erin@example.com")
	erin := ids.From[ids.UserKind](erinRaw)

	const objectClass = "contact"
	const externalID = "ext-ownerw-1"
	if err := store.UpsertUserMap(ctx, erin, "hubspot", "owner-w", "email"); err != nil {
		t.Fatalf("mapping erin to owner-w by email: %v", err)
	}
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields: map[string]any{"firstname": "OwnerW"}, ModifiedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		OwnerExternalID: "owner-w",
	}); err != nil {
		t.Fatalf("ingesting the owner-w record: %v", err)
	}
	if _, err := store.Get(ctxErin, objectClass, externalID); err != nil {
		t.Fatalf("before the email change, erin must see owner-w's record, got: %v", err)
	}

	// owner-w's email changes; NO record is reassigned or re-ingested.
	emails["owner-w"] = "not-erin@example.com"

	// Simulate the periodic reconcile sweep's own revalidation pass — the
	// same call reconcileConnection makes once per connection per tick,
	// with a resolver bound to the live incumbent adapter (here, the same
	// mutated stub).
	if err := store.RevalidateEmailMappings(ctx, emails); err != nil {
		t.Fatalf("periodic email-mapping revalidation: %v", err)
	}

	if _, err := store.Get(ctxErin, objectClass, externalID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("after the periodic sweep, erin's stale mapping must be dropped and her access revoked, got: %v", err)
	}
}

// seedStaleEmailMap inserts one mirror_user_map row directly, bypassing
// UpsertUserMap's own email check — it exists to fixture a row that WAS
// valid in the past and has since gone stale, which is a state
// UpsertUserMap itself (which always verifies on write) can never
// produce.
func seedStaleEmailMap(ctx context.Context, pool *pgxpool.Pool, appUser ids.UserID, incumbentUserID string) error {
	return database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO mirror_user_map (workspace_id, app_user_id, incumbent, incumbent_user_id, match_source)
			VALUES (NULLIF(current_setting('app.workspace_id',true),'')::uuid, $1, 'hubspot', $2, 'email')`,
			appUser, incumbentUserID)
		return err
	})
}
