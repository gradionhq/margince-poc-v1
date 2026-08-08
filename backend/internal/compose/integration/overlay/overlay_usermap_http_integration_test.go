// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// The admin user-map remedy loop, end to end over the composed overlay
// service and a real migrated Postgres. Overlay visibility is fail-closed:
// a user with no mirror_user_map row sees nothing at all. So an admin whose
// email matches no incumbent owner lands in an empty CRM, and until the
// user-map surface existed the only way out was an edit against the
// database. This proves the whole way out — the surface names the reason,
// the pin restores the records, the unmap takes them away again — and,
// last, that a deliberate unmap SURVIVES the automatic email-matching sweep
// that would otherwise re-map the admin behind their back.
//
// The fake incumbent stands in for HubSpot at both seams it is needed at:
// as the Incumbent the owners directory and the backfill are read from, and
// as the OwnerEmailResolver the email-sourced mapping is verified against
// (stubOwnerEmails, this suite's other resolver, always answers "" and so
// could never let the sweep match anyone).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	overlaymod "github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The incumbent-side fixture. recordOwner owns the one mirrored contact and
// carries an email nobody in this workspace has; adminOwner is the owner
// that LATER turns up carrying the admin's own address, which is what makes
// the closing sweep dangerous; controlOwner carries a second workspace
// user's address and is the live control that proves the same sweep really
// does map a matching user.
const (
	recordOwner  = "owner-1"
	adminOwner   = "owner-2"
	controlOwner = "owner-3"

	// mirroredContactID is the incumbent's own id for the mirrored contact —
	// numeric, like HubSpot's.
	mirroredContactID = "100214862111"

	// unmatchedOwnerEmail belongs to no app_user in this workspace, which is
	// precisely why the admin starts out unmapped.
	unmatchedOwnerEmail = "sales.ops@incumbent.test"
)

// incumbentEpoch is the fixed modified-at every fixture record carries. A
// literal keeps the backfill and the mirror baseline deterministic — a
// wall-clock stamp would make the run depend on when it happened (T11).
var incumbentEpoch = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// overlayUserMapAdminPerms is what the user-map surface actually gates on:
// overlay_connection ActionUpdate (requireUserMapAdmin demands UPDATE, not
// READ — every role holds the read grant, and this surface carries every
// user's email plus the incumbent's own directory), plus ActionCreate for
// the Connect that stands the connection up. overlayReaderPerms cannot be
// reused here: it grants no overlay_connection access at all, deliberately,
// because the mirror READ path gates on the visibility join rather than on
// object RBAC.
var overlayUserMapAdminPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"overlay_connection": {Create: true, Read: true, Update: true},
	},
	RowScope: principal.RowScopeAll,
}

// overlayUserMapAdminCtx binds user as a HUMAN admin of ws — human because
// requireUserMapAdmin refuses a passport outright, the gate the contract's
// mutating-methods-only agent check cannot cover for a read.
func overlayUserMapAdminCtx(ws, user ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		Permissions: overlayUserMapAdminPerms,
	})
}

// overlayAppUserEmail reads a seeded user's address back out of app_user.
// The closing sweep has to match on the value the database actually holds:
// seeding the incumbent owner from a hand-written copy of it would make the
// final assertion pass just as happily on a typo, proving nothing.
func overlayAppUserEmail(t *testing.T, user ids.UUID) string {
	t.Helper()
	var email string
	if err := integration.OwnerConn(t).QueryRow(context.Background(),
		`SELECT email FROM app_user WHERE id = $1`, user).Scan(&email); err != nil {
		t.Fatalf("reading %s's stored email: %v", user, err)
	}
	return email
}

// requireContactHidden asserts the mirrored contact resolves to nothing for
// ctx's principal. Fail-closed is the answer at three separate points of
// the loop below — before any mapping exists, after the unmap revokes one,
// and after the sweep declines to restore it — and each of them means the
// same thing: existence-hiding ErrNotFound, never an empty-but-successful
// read and never a 403.
func requireContactHidden(ctx context.Context, t *testing.T, store *overlaymod.MirrorStore, why string) {
	t.Helper()
	if _, err := store.Get(ctx, "person", mirroredContactID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("mirror read = %v, want apperrors.ErrNotFound — %s", err, why)
	}
}

// userMapViewFor finds one user's row on an admin mapping page.
func userMapViewFor(t *testing.T, page overlaymod.UserMapPage, user ids.UUID) overlaymod.UserMapView {
	t.Helper()
	for _, v := range page.Entries {
		if v.AppUserID == ids.From[ids.UserKind](user) {
			return v
		}
	}
	t.Fatalf("user %s does not appear on the mapping page (%d entries) — the surface an admin acts on must list them", user, len(page.Entries))
	return overlaymod.UserMapView{}
}

// TestAnUnmappedAdminMapsThemselvesAndTheRecordsAppear walks the remedy an
// admin locked out of their own mirrored records actually takes, and ends
// on the invariant the whole surface hangs on: an unmap is a decision, and
// the sweep does not get to undo it.
func TestAnUnmappedAdminMapsThemselvesAndTheRecordsAppear(t *testing.T) {
	e := integration.Setup(t)
	ws, admin := seedOverlayModeWorkspace(t)
	adminCtx := overlayUserMapAdminCtx(ws, admin)
	adminUser := ids.From[ids.UserKind](admin)

	fakeInc := fake.New()
	fakeInc.SeedOwner(recordOwner, unmatchedOwnerEmail)

	store := overlaymod.NewMirrorStore(e.Pool, fakeInc)
	svc := overlaymod.NewService(e.Pool, keyvault.NewMemory(), store).
		WithIncumbentFactory(func(string, string) overlaymod.Incumbent { return fakeInc })

	// The connection is what makes the owners directory readable, and the
	// directory is what lets the page tell "no owner carries this email"
	// apart from "we could not look".
	if _, err := svc.Connect(adminCtx, overlaymod.ConnectInput{
		Incumbent: "hubspot", Region: "eu1", Token: "pat-served-only-by-the-fake",
	}); err != nil {
		t.Fatalf("connecting the fake incumbent: %v", err)
	}

	// --- the incumbent owns one contact, through an owner who is not the
	// admin: mirrored through the real backfill seam, not hand-inserted ---
	fakeInc.Seed(overlaymod.IncumbentClassContacts, overlaymod.Record{
		ObjectClass:     "person",
		ExternalID:      mirroredContactID,
		Fields:          map[string]any{"first_name": "Ada", "last_name": "Overlay"},
		ModifiedAt:      incumbentEpoch,
		OwnerExternalID: recordOwner,
	})
	if _, err := overlaymod.Backfill(adminCtx, fakeInc, store, overlaymod.IncumbentClassContacts, incumbentEpoch); err != nil {
		t.Fatalf("backfilling the fake incumbent's contacts: %v", err)
	}

	// --- fail-closed: the row exists, and the admin cannot see it ---
	requireContactHidden(adminCtx, t, store, "an unmapped admin sees nothing; overlay visibility is fail-closed")

	// --- the surface says WHY, which is the difference between an empty CRM
	// and an actionable one ---
	page, err := svc.UserMap(adminCtx, "", 50)
	if err != nil {
		t.Fatalf("reading the admin mapping page: %v", err)
	}
	if page.Incumbent != "hubspot" {
		t.Errorf("mapping page incumbent = %q, want hubspot", page.Incumbent)
	}
	view := userMapViewFor(t, page, admin)
	if view.UnmappedReason != "no_email_match" {
		t.Fatalf("unmapped reason = %q, want no_email_match — no incumbent owner carries this admin's address", view.UnmappedReason)
	}

	// --- the remedy: a manual pin, the escape hatch the surface exists for ---
	if err := svc.SetUserMap(adminCtx, adminUser, recordOwner); err != nil {
		t.Fatalf("pinning the admin to %s: %v", recordOwner, err)
	}

	row, err := store.Get(adminCtx, "person", mirroredContactID)
	if err != nil {
		t.Fatalf("the mirrored contact must be visible once the admin is mapped to its owner: %v", err)
	}
	if row.Fields["first_name"] != "Ada" {
		t.Fatalf("visible mirror row = %+v, want the backfilled contact", row.Fields)
	}

	// --- and back: unmapping revokes the visibility the pin granted ---
	if err := svc.UnmapUser(adminCtx, adminUser); err != nil {
		t.Fatalf("unmapping the admin: %v", err)
	}
	requireContactHidden(adminCtx, t, store, "the unmap must revoke the grants the pin produced")

	// --- the point of the whole change: the incumbent directory now carries
	// an owner with the admin's OWN address, so the next sweep would match
	// them on email. The admin's unmap has to outrank that, or the remedy
	// silently reverses itself on a timer nobody watches. ---
	control := seedUnmappedAppUser(t, ws)
	fakeInc.SeedOwner(adminOwner, overlayAppUserEmail(t, admin))
	fakeInc.SeedOwner(controlOwner, overlayAppUserEmail(t, control))

	owners, err := fakeInc.Owners(context.Background())
	if err != nil {
		t.Fatalf("reading the incumbent owners directory: %v", err)
	}
	if err := store.SeedUserMap(adminCtx, "hubspot", owners); err != nil {
		t.Fatalf("running the email-matching sweep: %v", err)
	}

	swept, err := svc.UserMap(adminCtx, "", 50)
	if err != nil {
		t.Fatalf("reading the mapping page after the sweep: %v", err)
	}
	// The control first: a sweep that mapped nobody would make the admin's
	// still-hidden records prove nothing at all.
	controlView := userMapViewFor(t, swept, control)
	if controlView.IncumbentUserID != controlOwner || controlView.MatchSource != "email" {
		t.Fatalf("the control user's mapping = %q/%q, want %s/email — the sweep must genuinely match on address, or this test is vacuous",
			controlView.IncumbentUserID, controlView.MatchSource, controlOwner)
	}

	adminView := userMapViewFor(t, swept, admin)
	if adminView.IncumbentUserID != "" {
		t.Fatalf("the sweep re-mapped the admin to %q — an admin's unmap must outrank automatic email matching", adminView.IncumbentUserID)
	}
	if !adminView.Blocked || adminView.UnmappedReason != "blocked_by_admin" {
		t.Errorf("post-sweep admin row = blocked:%v/%q, want blocked:true/blocked_by_admin — the page must name the decision it is honoring",
			adminView.Blocked, adminView.UnmappedReason)
	}
	requireContactHidden(adminCtx, t, store, "the sweep must not restore visibility an admin removed")
	requireContactHidden(overlayActorCtx(ws, control), t, store,
		"being mapped grants nothing beyond your own owner's records")
}
