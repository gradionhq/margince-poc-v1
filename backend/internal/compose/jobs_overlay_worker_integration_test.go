// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// overlayReconcileWorker.Work's own real-Postgres proof: the
// empty-fleet path (no workspace has ever connected an incumbent) is
// the honest common case in any environment before the first
// connection is made — DueOverlayConnections' fleet-wide enumeration
// itself needs a real, migrated Postgres (workspace is not
// workspace-scoped, so this is not something a fake/mock can stand in
// for), and the loop body correctly doing nothing over zero due
// connections is exactly what this proves. A live-fetch success path
// would need a real HubSpot account (or a product-code seam this task
// does not add) and is out of scope here.

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// overlayAdminCtx binds a workspace admin with the overlay_connection
// grant Connect requires (AdminPerms in the shared harness deliberately
// omits it), acting as a REAL app_user so its email can seed-match.
func overlayAdminCtx(ws, user ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects:  map[string]principal.ObjectGrant{"overlay_connection": {Create: true, Read: true, Update: true, Delete: true}},
			RowScope: principal.RowScopeAll,
		},
	})
}

// overlayReaderCtx binds a plain workspace member acting as user — the
// UserID the mirror-store visibility deny-join keys can_see on. No object
// grant is needed: a mirror read gates on the visibility projection, not
// an RBAC object.
func overlayReaderCtx(ws, user ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		SeatType: principal.SeatFull, Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
}

func TestOverlayReconcileWorkerWorkNoOpsOverAnEmptyFleet(t *testing.T) {
	e := integration.Setup(t)

	w := &overlayReconcileWorker{
		pool:  e.Pool,
		vault: keyvault.NewMemory(),
		ms:    overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{}),
		meter: overlay.NewMeter(overlay.DefaultMeterConfig()),
		log:   slog.New(slog.DiscardHandler),
	}

	if err := w.Work(e.Admin(), nil); err != nil {
		t.Fatalf("Work over an empty fleet: %v", err)
	}
}

// TestResolveOverlayIncumbentBuildsALiveAdapterFromTheVault proves the
// per-request live-incumbent resolver the api server injects into the
// force-fresh read path: with no vault, or no active connection, it
// degrades to a nil adapter (force-fresh falls back to the mirror); once a
// HubSpot overlay is connected it unseals the token and returns a live
// adapter. Adapter construction reaches no network, so this needs no real
// HubSpot.
func TestResolveOverlayIncumbentBuildsALiveAdapterFromTheVault(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	adminCtx := overlayAdminCtx(e.WS, e.Rep1)

	// No vault wired → nil adapter, no error (honest degrade).
	if inc, err := (&Server{}).resolveOverlayIncumbent(e.Pool)(adminCtx); err != nil || inc != nil {
		t.Fatalf("resolve with no vault = (%v, %v), want (nil, nil)", inc, err)
	}

	resolve := (&Server{vault: vault}).resolveOverlayIncumbent(e.Pool)

	// Vault wired but no active connection → still nil (degrade).
	if inc, err := resolve(adminCtx); err != nil || inc != nil {
		t.Fatalf("resolve with no connection = (%v, %v), want (nil, nil)", inc, err)
	}

	// Connect a HubSpot overlay; now resolve builds a live adapter.
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})
	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(adminCtx, overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	inc, err := resolve(adminCtx)
	if err != nil {
		t.Fatalf("resolve after connect: %v", err)
	}
	if inc == nil || inc.Name() != "hubspot" {
		t.Fatalf("resolve after connect = %v, want a live hubspot adapter", inc)
	}
}

// TestReconcileConnectionBackfillsAndSeedsViaFakeIncumbent proves the
// §6.2 sweep end to end against a fake incumbent (the seam the factory
// injection now enables — before it, reconcileConnection hardcoded a real
// hubspot.Adapter and no test could drive its success path): one sweep
// backfills the object class (making SyncStatus's backfillComplete
// truthful) AND seeds mirror_user_map from the owners directory (§6.1's
// reconcile-lane path), so a matched user sees the backfilled record while
// an unmatched one stays hidden. It is the runnable-end-to-end proof the
// read review asked for: connect -> sweep -> a mapped user reads mirrored
// rows through the ordinary store, with zero manual UpsertUserMap/Backfill
// priming.
func TestReconcileConnectionBackfillsAndSeedsViaFakeIncumbent(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})

	// Connect an overlay for the workspace. No connect-time incumbent
	// factory is wired on this Service, so NOTHING is seeded or backfilled
	// until the sweep runs — exactly the behavior under test.
	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// The fake incumbent: one contacts record owned by owner-1, whose
	// directory email is Rep1's (a@authz.test — the shared harness seeds
	// Rep1/Rep2/Rep3 as a/b/c@authz.test).
	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "a@authz.test")
	rec := fake.Rec("c-1", map[string]any{"firstname": "Ada"})
	rec.ObjectClass = "person" // canonical — the mapping adapter's own translation, simulated
	rec.OwnerExternalID = "owner-1"
	fakeInc.Seed(overlay.IncumbentClassContacts, rec)

	due, err := overlay.DueOverlayConnections(overlayAdminCtx(e.WS, e.Rep1), e.Pool)
	if err != nil {
		t.Fatalf("DueOverlayConnections: %v", err)
	}
	var d overlay.DueOverlayConnection
	for _, c := range due {
		if c.Workspace.UUID == e.WS {
			d = c
		}
	}
	if d.Incumbent == "" {
		t.Fatal("no due overlay connection for the workspace after connect")
	}

	sweepCtx := reconcileWorkerCtx(context.Background(), ids.From[ids.WorkspaceKind](e.WS))
	if err := reconcileConnection(sweepCtx, vault, ms, overlay.NewMeter(overlay.DefaultMeterConfig()),
		slog.New(slog.DiscardHandler), d, func(_, _ string) overlay.Incumbent { return fakeInc }); err != nil {
		t.Fatalf("reconcileConnection: %v", err)
	}

	// Backfill ran to completion: SyncStatus's backfillComplete is truthful.
	if _, done, err := ms.LoadBackfillCursor(sweepCtx, overlay.IncumbentClassContacts); err != nil {
		t.Fatalf("LoadBackfillCursor: %v", err)
	} else if !done {
		t.Fatal("contacts backfill cursor is not done after the sweep — backfill did not run")
	}

	// Seeding mapped Rep1 to owner-1, so Rep1 sees the backfilled record.
	if _, err := ms.Get(overlayReaderCtx(e.WS, e.Rep1), "person", "c-1"); err != nil {
		t.Fatalf("Rep1 (seed-matched) must see the backfilled record, got: %v", err)
	}
	// Rep2 matches no owner, so stays hidden (existence-hiding 404).
	if _, err := ms.Get(overlayReaderCtx(e.WS, e.Rep2), "person", "c-1"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("Rep2 (unmapped) must not see the record, got: %v", err)
	}
}

// TestReconcileConnectionPurgesIncumbentDeletedRecord proves the deletion
// feed end to end through the poller's shared sweep: a first sweep mirrors
// a record a mapped user can read; the incumbent then deletes it; the next
// sweep purges it, and the same user can no longer read it. This is the
// runnable proof that an incumbent-side deletion stops being visible in
// overlay mode rather than lingering until disconnect (branch-1b).
func TestReconcileConnectionPurgesIncumbentDeletedRecord(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})

	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "a@authz.test")
	rec := fake.Rec("990009", map[string]any{"firstname": "Ada"})
	rec.ObjectClass = "person" // canonical — the mapping adapter's own translation, simulated
	rec.OwnerExternalID = "owner-1"
	fakeInc.Seed(overlay.IncumbentClassContacts, rec)

	due, err := overlay.DueOverlayConnections(overlayAdminCtx(e.WS, e.Rep1), e.Pool)
	if err != nil {
		t.Fatalf("DueOverlayConnections: %v", err)
	}
	var d overlay.DueOverlayConnection
	for _, c := range due {
		if c.Workspace.UUID == e.WS {
			d = c
		}
	}
	if d.Incumbent == "" {
		t.Fatal("no due overlay connection for the workspace after connect")
	}

	sweepCtx := reconcileWorkerCtx(context.Background(), ids.From[ids.WorkspaceKind](e.WS))
	newInc := func(_, _ string) overlay.Incumbent { return fakeInc }
	meter := overlay.NewMeter(overlay.DefaultMeterConfig())

	// First sweep mirrors the live record; the mapped reader can see it.
	if err := reconcileConnection(sweepCtx, vault, ms, meter, slog.New(slog.DiscardHandler), d, newInc); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if _, err := ms.Get(overlayReaderCtx(e.WS, e.Rep1), "person", "990009"); err != nil {
		t.Fatalf("Rep1 must see the record after the first sweep: %v", err)
	}

	// The incumbent archives the record (it leaves the live feed and enters
	// the deletion feed); the next sweep must purge it from the mirror.
	fakeInc.SeedDeletion(overlay.IncumbentClassContacts, overlay.Deletion{
		ExternalID: "990009", ObjectClass: "person", DeletedAt: rec.ModifiedAt.Add(time.Hour),
	})
	if err := reconcileConnection(sweepCtx, vault, ms, meter, slog.New(slog.DiscardHandler), d, newInc); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if _, err := ms.Get(overlayReaderCtx(e.WS, e.Rep1), "person", "990009"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("Rep1 must NOT see the record after it was deleted incumbent-side, got: %v", err)
	}
}
