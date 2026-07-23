// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedIncumbent is a minimal inline overlay.Incumbent whose only
// meaningful behavior is the owners directory + single-owner email
// resolution the Connect-time seeding exercises; every record/association
// seam method is an unused no-op, since a connect-time seed matches
// owners to users, it never sweeps records. It lives inline (not
// overlay/fake) because a package-overlay test cannot import a package
// that imports overlay.
type seedIncumbent struct{ owners map[string]string }

func (seedIncumbent) Name() string { return "hubspot" }
func (seedIncumbent) Backfill(context.Context, string, string) (Page, error) {
	return Page{}, nil
}

func (seedIncumbent) Modified(context.Context, string, time.Time, string) (Page, error) {
	return Page{}, nil
}

func (seedIncumbent) Deletions(context.Context, string, time.Time, string) (DeletionPage, error) {
	return DeletionPage{}, nil
}
func (seedIncumbent) Get(context.Context, string, string) (Record, error) { return Record{}, nil }
func (seedIncumbent) Associations(context.Context, string, string, string) ([]Assoc, error) {
	return nil, nil
}

func (s seedIncumbent) OwnerEmail(_ context.Context, ownerExternalID string) (string, error) {
	email, ok := s.owners[ownerExternalID]
	if !ok {
		return "", fmt.Errorf("seedIncumbent: no owner with external id %s", ownerExternalID)
	}
	return email, nil
}

func (s seedIncumbent) Owners(context.Context) ([]OwnerRef, error) {
	out := make([]OwnerRef, 0, len(s.owners))
	for id, email := range s.owners {
		out = append(out, OwnerRef{ExternalID: id, Email: email})
	}
	return out, nil
}

func (seedIncumbent) Create(context.Context, string, map[string]any) (Record, error) {
	return Record{}, fmt.Errorf("seedIncumbent: Create is not fixtured")
}

func (seedIncumbent) Update(context.Context, string, string, map[string]any, time.Time) (Record, error) {
	return Record{}, fmt.Errorf("seedIncumbent: Update is not fixtured")
}

func (seedIncumbent) Archive(context.Context, string, string) error {
	return fmt.Errorf("seedIncumbent: Archive is not fixtured")
}

// TestConnectSeedsUserMapFromTheOwnersDirectory proves §6.1's primary
// trigger: connecting an overlay pulls the incumbent's owners directory
// and seeds mirror_user_map immediately, so a matched user sees the
// already-mirrored rows without waiting for the first reconcile sweep,
// while an unmatched user still sees nothing (fail-closed).
func TestConnectSeedsUserMapFromTheOwnersDirectory(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	vault := keyvault.NewMemory()
	store := NewMirrorStore(pool, noOwnerEmails{})
	svc := NewService(pool, vault, store).
		WithIncumbentFactory(func(_, _ string) Incumbent {
			return seedIncumbent{owners: map[string]string{"owner-alice": "alice@example.com"}}
		})

	ctxAlice, _ := testWorkspaceCtxAsUser(t, ws, "alice@example.com")
	ctxBob, _ := testWorkspaceCtxAsUser(t, ws, "bob@example.com")

	// A record already mirrored (as an earlier backfill would leave it)
	// but visible to no one yet, because nothing has mapped its owner.
	const objectClass, external = "contact", "ext-alice"
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: external,
		Fields: map[string]any{"firstname": "Backfilled"}, ModifiedAt: time.Now(),
		OwnerExternalID: "owner-alice",
	}); err != nil {
		t.Fatalf("pre-seeding ingest: %v", err)
	}

	if _, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if _, err := store.Get(ctxAlice, objectClass, external); err != nil {
		t.Fatalf("alice must see her record after connect-time seeding, got: %v", err)
	}
	if _, err := store.Get(ctxBob, objectClass, external); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("bob must stay unmapped and hidden after connect, got: %v", err)
	}
}

// TestSeedUserMapMatchesOwnersToUsersByEmail proves the §6.1 seeding
// primitive: given the incumbent's owners directory (owner id → email),
// SeedUserMap writes a mirror_user_map row for every owner whose email
// matches an existing workspace app_user (design.md §4.6: a MATCH, never
// an import) and NONE for an owner with no matching user — the
// fail-closed rule that keeps a connected overlay from granting a record
// to a user the incumbent never actually owns-through. It is the missing
// writer the read review flagged: without it, a connected workspace
// serves zero rows because nothing populates mirror_user_map.
func TestSeedUserMapMatchesOwnersToUsersByEmail(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	// The store's resolver mirrors the directory it will be seeded from:
	// UpsertUserMap re-verifies each proposed pairing against the
	// incumbent's current owner email, so the two must agree.
	store := NewMirrorStore(pool, stubOwnerEmails{
		"owner-alice": "alice@example.com",
		"owner-carol": "carol@example.com",
	})

	ctxAlice, _ := testWorkspaceCtxAsUser(t, ws, "  Alice@Example.com  ")
	ctxBob, _ := testWorkspaceCtxAsUser(t, ws, "bob@example.com")

	const objectClass = "contact"
	const aliceRecord = "ext-alice-owned"
	const carolRecord = "ext-carol-owned"
	for owner, external := range map[string]string{"owner-alice": aliceRecord, "owner-carol": carolRecord} {
		if err := store.Ingest(ctx, Record{
			ObjectClass: objectClass, ExternalID: external,
			Fields: map[string]any{"firstname": "Seeded"}, ModifiedAt: time.Now(),
			OwnerExternalID: owner,
		}); err != nil {
			t.Fatalf("ingesting the %s record: %v", owner, err)
		}
	}

	// The owners directory carries a third owner (owner-dave) that no
	// workspace user matches — SeedUserMap must skip it, never guess.
	owners := []OwnerRef{
		{ExternalID: "owner-alice", Email: "alice@example.com"},
		{ExternalID: "owner-carol", Email: "carol@nobody.example.com"},
		{ExternalID: "owner-dave", Email: "dave@example.com"},
	}
	if err := store.SeedUserMap(ctx, "hubspot", owners); err != nil {
		t.Fatalf("SeedUserMap: %v", err)
	}

	// Alice's directory email matches her app_user email (case/whitespace
	// normalized) — she is mapped and sees her record.
	if _, err := store.Get(ctxAlice, objectClass, aliceRecord); err != nil {
		t.Fatalf("alice must see the record she was seed-matched to, got: %v", err)
	}

	// Bob exists but no owner's email matches his — he is unmapped and
	// sees nothing (existence-hiding 404).
	if _, err := store.Get(ctxBob, objectClass, aliceRecord); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("bob must stay unmapped and hidden, got: %v", err)
	}

	// owner-carol's DIRECTORY email (carol@nobody...) does not match the
	// carol@example.com app_user, so no row is written — fail-closed, and
	// alice (mapped to owner-alice) must not gain carol's record either.
	if _, err := store.Get(ctxAlice, objectClass, carolRecord); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("no user should see owner-carol's record (no email match), got: %v", err)
	}
}

// TestSeedUserMapNeverOverwritesAManualMapping proves
// an admin's manual override is sticky against sweep seeding. A user
// manually mapped to owner-X, whose email ALSO matches owner-Y in the
// directory, must keep owner-X — seeding must not remap them to owner-Y
// (which would revoke owner-X's records the escape hatch exists to grant).
func TestSeedUserMapNeverOverwritesAManualMapping(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{
		"owner-x": "someone-else@example.com",
		"owner-y": "alice@example.com",
	})
	ctxAlice, aliceRaw := testWorkspaceCtxAsUser(t, ws, "alice@example.com")
	alice := ids.From[ids.UserKind](aliceRaw)

	const objectClass = "contact"
	const recX, recY = "ext-owned-by-x", "ext-owned-by-y"
	for owner, external := range map[string]string{"owner-x": recX, "owner-y": recY} {
		if err := store.Ingest(ctx, Record{
			ObjectClass: objectClass, ExternalID: external,
			Fields: map[string]any{"firstname": "Rec"}, ModifiedAt: time.Now(), OwnerExternalID: owner,
		}); err != nil {
			t.Fatalf("ingesting the %s record: %v", owner, err)
		}
	}

	// Admin manually maps alice to owner-x (manual bypasses the email check).
	if err := store.UpsertUserMap(ctx, alice, "hubspot", "owner-x", "manual"); err != nil {
		t.Fatalf("manual mapping alice to owner-x: %v", err)
	}

	// A sweep seeds from the directory, where owner-y carries alice's email.
	if err := store.SeedUserMap(ctx, "hubspot", []OwnerRef{{ExternalID: "owner-y", Email: "alice@example.com"}}); err != nil {
		t.Fatalf("SeedUserMap: %v", err)
	}

	// The manual override held: alice still sees owner-x's record...
	if _, err := store.Get(ctxAlice, objectClass, recX); err != nil {
		t.Fatalf("alice must keep her manual mapping to owner-x, got: %v", err)
	}
	// ...and was NOT remapped to owner-y.
	if _, err := store.Get(ctxAlice, objectClass, recY); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("seeding must not remap a manual mapping to owner-y, got: %v", err)
	}
}

// TestSeedUserMapRevokesAMappingThatBecameAmbiguous proves the
// ambiguity rule holds GOING FORWARD, not only at first seed: a user
// mapped while their owner's email was unique must LOSE that mapping (and
// its visibility grants) once a second incumbent owner acquires the same
// email — otherwise a user keeps access through a match that is no longer
// unambiguous (design.md §4.6 "ambiguous → no row"). Skipping the
// re-seed is not enough; the pre-existing row must be revoked.
func TestSeedUserMapRevokesAMappingThatBecameAmbiguous(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-p": "bob@example.com"})
	ctxBob, _ := testWorkspaceCtxAsUser(t, ws, "bob@example.com")

	const objectClass, external = "contact", "ext-owned-by-p"
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: external,
		Fields: map[string]any{"firstname": "Rec"}, ModifiedAt: time.Now(), OwnerExternalID: "owner-p",
	}); err != nil {
		t.Fatalf("ingesting the owner-p record: %v", err)
	}

	// First sweep: owner-p alone carries bob@ — bob is seed-matched and sees it.
	if err := store.SeedUserMap(ctx, "hubspot", []OwnerRef{{ExternalID: "owner-p", Email: "bob@example.com"}}); err != nil {
		t.Fatalf("SeedUserMap (unique): %v", err)
	}
	if _, err := store.Get(ctxBob, objectClass, external); err != nil {
		t.Fatalf("bob must see the record after the unique-email seed, got: %v", err)
	}

	// A second owner acquires bob's email — the match is now AMBIGUOUS.
	// The next sweep must revoke bob's stale mapping, not merely skip re-seeding.
	if err := store.SeedUserMap(ctx, "hubspot", []OwnerRef{
		{ExternalID: "owner-p", Email: "bob@example.com"},
		{ExternalID: "owner-q", Email: "bob@example.com"},
	}); err != nil {
		t.Fatalf("SeedUserMap (now ambiguous): %v", err)
	}
	if _, err := store.Get(ctxBob, objectClass, external); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("bob's mapping must be revoked once his email became ambiguous, got: %v", err)
	}
}

// TestSeedUserMapTreatsADuplicateOwnerListingAsOneOwner proves the
// ambiguity check counts DISTINCT owners, not raw directory entries: a
// paginated owners directory can list the same owner twice (overlapping
// pages), and that must still seed the single legitimate owner — never be
// misread as two owners sharing the email and revoked/withheld.
func TestSeedUserMapTreatsADuplicateOwnerListingAsOneOwner(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-p": "bob@example.com"})
	ctxBob, _ := testWorkspaceCtxAsUser(t, ws, "bob@example.com")

	const objectClass, external = "contact", "ext-owned-by-p"
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: external,
		Fields: map[string]any{"firstname": "Rec"}, ModifiedAt: time.Now(), OwnerExternalID: "owner-p",
	}); err != nil {
		t.Fatalf("ingesting the owner-p record: %v", err)
	}

	// owner-p listed TWICE — one owner, not an ambiguous pair.
	if err := store.SeedUserMap(ctx, "hubspot", []OwnerRef{
		{ExternalID: "owner-p", Email: "bob@example.com"},
		{ExternalID: "owner-p", Email: "bob@example.com"},
	}); err != nil {
		t.Fatalf("SeedUserMap: %v", err)
	}
	if _, err := store.Get(ctxBob, objectClass, external); err != nil {
		t.Fatalf("bob must be seed-matched to the single owner-p (a duplicate listing is not ambiguity), got: %v", err)
	}
}

// TestSeedUserMapSkipsAmbiguousEmail proves that two
// directory owners sharing one email is an AMBIGUOUS match — no row is
// written (design.md §4.6 / review §3: "zero OR ambiguous → no row"),
// rather than a nondeterministic remap to whichever owner listed last.
func TestSeedUserMapSkipsAmbiguousEmail(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{
		"owner-p": "bob@example.com",
		"owner-q": "bob@example.com",
	})
	ctxBob, _ := testWorkspaceCtxAsUser(t, ws, "bob@example.com")

	const objectClass, external = "contact", "ext-owned-by-p"
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: external,
		Fields: map[string]any{"firstname": "Rec"}, ModifiedAt: time.Now(), OwnerExternalID: "owner-p",
	}); err != nil {
		t.Fatalf("ingesting the owner-p record: %v", err)
	}

	if err := store.SeedUserMap(ctx, "hubspot", []OwnerRef{
		{ExternalID: "owner-p", Email: "bob@example.com"},
		{ExternalID: "owner-q", Email: "bob@example.com"},
	}); err != nil {
		t.Fatalf("SeedUserMap: %v", err)
	}

	// Ambiguous email → no mapping → bob sees nothing.
	if _, err := store.Get(ctxBob, objectClass, external); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an ambiguous email must seed no mapping, got: %v", err)
	}
}
