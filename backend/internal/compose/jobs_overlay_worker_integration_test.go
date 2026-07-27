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
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// workerBudgetMeter builds a Redis-backed OVB meter for the "fake"
// incumbent these worker tests sweep through — the poller's reconcile
// reserves a search slot per page, so an unconfigured or fail-closed meter
// would pace the sweep to a stop before it mirrors anything. The
// raw-Redis dependency lives in budgettest (platform tier), never here.
func workerBudgetMeter(t *testing.T) *overlaybudget.Meter {
	t.Helper()
	return budgettest.Meter(t, budgettest.SmallConfig("fake"))
}

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
		meter: workerBudgetMeter(t),
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

// authFailingIncumbent is a fake whose owners-directory fetch fails with a
// connection-level auth error (the first incumbent call a sweep makes),
// standing in for a revoked/insufficient HubSpot token. Every other method
// delegates to the embedded fake (unused by this test's path).
type authFailingIncumbent struct{ *fake.Adapter }

func (authFailingIncumbent) Owners(context.Context) ([]overlay.OwnerRef, error) {
	return nil, apperrors.ErrPermissionDenied
}

// TestWorkerBacksOffAConnectionLevelFailure proves the branch-1b backoff
// end to end through the poller: a sweep that fails at the connection level
// (auth here) records a backoff, so DueOverlayConnections stops selecting
// that workspace on the next tick — no more re-sweeping a dead connection
// hot. Work itself returns nil (a single connection's failure never aborts
// the fleet pass).
func TestWorkerBacksOffAConnectionLevelFailure(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})
	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	w := &overlayReconcileWorker{
		pool: e.Pool, vault: vault, ms: ms,
		meter:        workerBudgetMeter(t),
		log:          slog.New(slog.DiscardHandler),
		newIncumbent: func(_, _ string) overlay.Incumbent { return authFailingIncumbent{Adapter: fake.New()} },
	}
	if err := w.Work(e.Admin(), nil); err != nil {
		t.Fatalf("Work must not error on a single connection's failure: %v", err)
	}

	// The workspace is now backed off — no longer due.
	due, err := overlay.DueOverlayConnections(e.Admin(), e.Pool)
	if err != nil {
		t.Fatalf("DueOverlayConnections: %v", err)
	}
	for _, d := range due {
		if d.Workspace.UUID == e.WS {
			t.Fatal("the workspace must be backed off after a connection-level sweep failure, but it is still due")
		}
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
	if err := reconcileConnection(sweepCtx, e.Pool, vault, ms, workerBudgetMeter(t),
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

// countMirrorRows answers how many overlay_mirror rows the workspace holds
// for a CANONICAL object class — the honest measure of how much of a portal
// a sweep actually pulled down.
func countMirrorRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool, objectClass string) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM overlay_mirror WHERE object_class = $1`, objectClass).Scan(&n)
	}); err != nil {
		t.Fatalf("counting %s mirror rows: %v", objectClass, err)
	}
	return n
}

// TestCappedBackfillIsNotUndoneByTheFirstModifiedSweep proves the
// MARGINCE_OVERLAY_BACKFILL_LIMIT dev cap actually bounds what a fresh
// connect pulls onto a laptop.
//
// The cap wraps Backfill only, so on its own it bounds nothing: a class with
// no checkpoint used to sweep from the zero time, which the HubSpot adapter
// renders as `lastmodifieddate GTE 0` — the whole portal, pulled by the very
// next Modified pass. A cap of 5 against a 250-record portal would land all
// 250 rows. Raising the sweep window to the connect instant is what closes
// that door, so this asserts the record COUNT, not merely that the cursor
// converged.
func TestCappedBackfillIsNotUndoneByTheFirstModifiedSweep(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})

	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// A portal far larger than the cap. Every record predates the connect, so
	// a correctly-derived floor puts all of them below the Modified pass —
	// only the capped backfill should land anything.
	const portal, backfillLimit = 250, 5
	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "a@authz.test")
	past := time.Now().Add(-24 * time.Hour)
	for i := range portal {
		rec := fake.Rec("c-"+strconv.Itoa(i), map[string]any{"firstname": "Ada"})
		rec.ObjectClass = "person"
		rec.OwnerExternalID = "owner-1"
		rec.ModifiedAt = past
		fakeInc.Seed(overlay.IncumbentClassContacts, rec)
	}

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
	capped := cappedIncumbent{Incumbent: fakeInc, limit: backfillLimit}
	// Sweep twice: the second tick is where a converged backfill steps aside
	// and the Modified pass runs alone — the exact tick that used to pull the
	// remaining 245 records down.
	for tick := 1; tick <= 2; tick++ {
		if err := reconcileConnection(sweepCtx, e.Pool, vault, ms, workerBudgetMeter(t),
			slog.New(slog.DiscardHandler), d, func(_, _ string) overlay.Incumbent { return capped }); err != nil {
			t.Fatalf("reconcileConnection tick %d: %v", tick, err)
		}
		if got := countMirrorRows(sweepCtx, t, e.Pool, "person"); got != backfillLimit {
			t.Fatalf("tick %d mirrored %d person rows, want exactly %d — the cap must bound the whole sweep, not just Backfill", tick, got, backfillLimit)
		}
	}

	// The cap's honesty half: a class it declined records for must never
	// report backfill-complete, even though its cursor is done=true (correct
	// — re-listing under the same cap would relearn nothing). Reporting
	// complete here would be the same silent-truncation-as-success lie this
	// whole fix exists to close, just for the cap instead of the watermark.
	svc := overlay.NewService(e.Pool, vault, ms).WithIncumbentClassesTranslator(hubspot.IncumbentClassesFor)
	status, err := svc.SyncStatus(overlayAdminCtx(e.WS, e.Rep1))
	if err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	found := false
	for _, o := range status {
		if o.Object != "person" {
			continue
		}
		found = true
		if o.BackfillComplete {
			t.Error("SyncStatus reports person backfillComplete=true after a capped backfill — the cap declined records the incumbent still has, this must be false")
		}
	}
	if !found {
		t.Fatal("SyncStatus reported no person object — expected the mirrored rows to produce one")
	}
}

// TestSweepStillIngestsRecordsEditedAfterTheConnect is the other half of the
// cap proof above: the floor must bound the first pass WITHOUT turning the
// poller into a no-op.
//
// It guards against over-correcting. Flooring too high, or capping the
// Modified pass itself, would also make the cap test pass — and would silently
// stop continuous sync, which is worse than the bug being fixed. A record the
// incumbent edits after the connect sits above the floor and must arrive.
func TestSweepStillIngestsRecordsEditedAfterTheConnect(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})

	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "a@authz.test")
	old := fake.Rec("c-old", map[string]any{"firstname": "Ada"})
	old.ObjectClass, old.OwnerExternalID, old.ModifiedAt = "person", "owner-1", time.Now().Add(-24*time.Hour)
	fakeInc.Seed(overlay.IncumbentClassContacts, old)

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
	sweep := func() {
		t.Helper()
		if err := reconcileConnection(sweepCtx, e.Pool, vault, ms, workerBudgetMeter(t),
			slog.New(slog.DiscardHandler), d, func(_, _ string) overlay.Incumbent { return fakeInc }); err != nil {
			t.Fatalf("reconcileConnection: %v", err)
		}
	}
	sweep()

	// The pre-existing record is 24h old, so it sits BELOW the floor and the
	// Modified pass skips it — the backfill already mirrored it. This one is
	// edited after the connect, so it is above the floor and must arrive.
	fresh := fake.Rec("c-new", map[string]any{"firstname": "Grace"})
	fresh.ObjectClass, fresh.OwnerExternalID, fresh.ModifiedAt = "person", "owner-1", time.Now().Add(time.Minute)
	fakeInc.Seed(overlay.IncumbentClassContacts, fresh)
	sweep()

	if _, err := ms.Get(overlayReaderCtx(e.WS, e.Rep1), "person", "c-new"); err != nil {
		t.Fatalf("a record modified after the connect must still be swept in, got: %v — the floor bounds the first pass, it must not stop continuous sync", err)
	}
}

// TestReconcileConnectionStopsCleanlyWhenDisconnectedMidSweep proves the
// disconnect-race fence end to end through the sweep orchestration: if the
// connection is revoked after the sweep resolved its token but before its
// writes land, reconcileConnection aborts with overlay.ErrConnectionGone —
// the clean-stop signal the worker turns into "skip this workspace, no
// backoff" — and resurrects nothing into the now-disconnected workspace.
func TestReconcileConnectionStopsCleanlyWhenDisconnectedMidSweep(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})

	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "a@authz.test")
	rec := fake.Rec("c-1", map[string]any{"firstname": "Ada"})
	rec.ObjectClass = "person" // canonical
	rec.OwnerExternalID = "owner-1"
	fakeInc.Seed(overlay.IncumbentClassContacts, rec)

	adminCtx := overlayAdminCtx(e.WS, e.Rep1)
	due, err := overlay.DueOverlayConnections(adminCtx, e.Pool)
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

	// Simulate a disconnect landing AFTER the sweep resolved its token: revoke
	// the connection row directly (leaving the vaulted token in place, so the
	// sweep's token resolution still succeeds and it proceeds to its first
	// fenced write, exactly the mid-sweep race the fence exists for).
	if err := database.WithWorkspaceTx(adminCtx, e.Pool, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(adminCtx,
			`UPDATE incumbent_connection SET status = 'revoked', revoked_at = now()
			 WHERE workspace_id = current_setting('app.workspace_id')::uuid`)
		return execErr
	}); err != nil {
		t.Fatalf("revoking the connection mid-sweep: %v", err)
	}

	sweepCtx := reconcileWorkerCtx(context.Background(), ids.From[ids.WorkspaceKind](e.WS))
	err = reconcileConnection(sweepCtx, e.Pool, vault, ms, workerBudgetMeter(t),
		slog.New(slog.DiscardHandler), d, func(_, _ string) overlay.Incumbent { return fakeInc })
	if !errors.Is(err, overlay.ErrConnectionGone) {
		t.Fatalf("reconcileConnection over a revoked connection = %v, want overlay.ErrConnectionGone (clean stop)", err)
	}

	// The fenced sweep resurrected nothing: no mirror row, no owner mapping.
	var mirrorRows, userMaps int
	if qErr := database.WithWorkspaceTx(sweepCtx, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(sweepCtx, `SELECT count(*) FROM overlay_mirror`).Scan(&mirrorRows); err != nil {
			return err
		}
		return tx.QueryRow(sweepCtx, `SELECT count(*) FROM mirror_user_map`).Scan(&userMaps)
	}); qErr != nil {
		t.Fatalf("counting resurrected rows: %v", qErr)
	}
	if mirrorRows != 0 || userMaps != 0 {
		t.Errorf("after a fenced sweep over a revoked connection: overlay_mirror=%d mirror_user_map=%d, want 0/0 — the fence must resurrect nothing", mirrorRows, userMaps)
	}
}

// TestReconcileConnectionStopsCleanlyWhenReconnectedMidSweep proves the
// identity fence (overlay's assertOwnConnection/assertFence, disconnectfence.go
// and mirrorstore.go's WithFenceIdentity): a sweep that resolved its
// due-connection identity BEFORE a disconnect+reconnect straddles that race
// exactly like a mid-sweep disconnect alone — every fenced write it issues
// (SeedUserMap's UpsertUserMap, Ingest, the backfill checkpoint) aborts with
// ErrConnectionGone rather than landing under the NEW connection's identity.
// Before WithFenceIdentity existed, the status-only fence would have let all
// of these SUCCEED (an active row exists either way): a stray mirror row and
// owner mapping resurrected under the wrong generation, plus a done=true
// backfill cursor for a connection whose own backfill never actually ran —
// permanently short-circuiting it, since a done cursor is never re-listed
// and ReconcileFloor stops the incremental sweep from ever re-reading the
// gap.
func TestReconcileConnectionStopsCleanlyWhenReconnectedMidSweep(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})

	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "a@authz.test")
	rec := fake.Rec("c-1", map[string]any{"firstname": "Ada"})
	rec.ObjectClass = "person"
	rec.OwnerExternalID = "owner-1"
	fakeInc.Seed(overlay.IncumbentClassContacts, rec)

	adminCtx := overlayAdminCtx(e.WS, e.Rep1)
	due, err := overlay.DueOverlayConnections(adminCtx, e.Pool)
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

	// Simulate a disconnect+reconnect landing AFTER the sweep resolved its
	// due-connection identity (d, above) but BEFORE its first checkpoint
	// write: revive the SAME row with a fresh connected_at — exactly what
	// reconnectConnection does to the row's identity — via raw SQL rather
	// than the real Disconnect+Connect flow, so the sweep's already-resolved
	// vaulted token stays valid (the same reason the sibling mid-sweep
	// disconnect test above revokes via raw SQL instead of svc.Disconnect).
	if err := database.WithWorkspaceTx(adminCtx, e.Pool, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(adminCtx, `
			UPDATE incumbent_connection SET connected_at = now()
			WHERE workspace_id = current_setting('app.workspace_id')::uuid`)
		return execErr
	}); err != nil {
		t.Fatalf("reconnecting mid-sweep: %v", err)
	}

	sweepCtx := reconcileWorkerCtx(context.Background(), ids.From[ids.WorkspaceKind](e.WS))
	err = reconcileConnection(sweepCtx, e.Pool, vault, ms, workerBudgetMeter(t),
		slog.New(slog.DiscardHandler), d, func(_, _ string) overlay.Incumbent { return fakeInc })
	if !errors.Is(err, overlay.ErrConnectionGone) {
		t.Fatalf("reconcileConnection straddling a reconnect = %v, want overlay.ErrConnectionGone (the identity fence)", err)
	}

	// The new connection's own backfill cursor must NOT have been left
	// done=true by the straddling sweep's checkpoint write — that would
	// permanently short-circuit its real backfill (Backfill's own
	// top-of-function short-circuit, backfill.go).
	if _, done, loadErr := ms.LoadBackfillCursor(sweepCtx, overlay.IncumbentClassContacts); loadErr != nil {
		t.Fatalf("LoadBackfillCursor: %v", loadErr)
	} else if done {
		t.Error("the straddling sweep's checkpoint write must not land done=true for the new connection")
	}

	// The fenced sweep resurrected nothing else either: no mirror row, no
	// owner mapping — the same proof TestReconcileConnectionStopsCleanlyWhenDisconnectedMidSweep
	// makes for a plain disconnect, now for a straddling reconnect.
	var mirrorRows, userMaps int
	if qErr := database.WithWorkspaceTx(sweepCtx, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(sweepCtx, `SELECT count(*) FROM overlay_mirror`).Scan(&mirrorRows); err != nil {
			return err
		}
		return tx.QueryRow(sweepCtx, `SELECT count(*) FROM mirror_user_map`).Scan(&userMaps)
	}); qErr != nil {
		t.Fatalf("counting resurrected rows: %v", qErr)
	}
	if mirrorRows != 0 || userMaps != 0 {
		t.Errorf("after a sweep straddling a reconnect: overlay_mirror=%d mirror_user_map=%d, want 0/0 — the identity fence must resurrect nothing under the new connection", mirrorRows, userMaps)
	}
}

// revokeOnOwnersIncumbent simulates a disconnect landing MID-SWEEP: it
// revokes the workspace's connection row (leaving the vaulted token in place)
// the first time the sweep calls Owners — after the due-scan enumerated the
// connection as active but before the sweep's first fenced write — then
// delegates to the wrapped fake. It is the deterministic hook that exercises
// the disconnect-race clean-stop paths (the fence itself is DB state, not an
// incumbent response, so it cannot be injected through the adapter directly).
type revokeOnOwnersIncumbent struct {
	overlay.Incumbent
	pool *pgxpool.Pool
	done bool
}

func (r *revokeOnOwnersIncumbent) Owners(ctx context.Context) ([]overlay.OwnerRef, error) {
	if !r.done {
		r.done = true
		if err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
			_, execErr := tx.Exec(ctx,
				`UPDATE incumbent_connection SET status = 'revoked', revoked_at = now()
				 WHERE workspace_id = current_setting('app.workspace_id')::uuid`)
			return execErr
		}); err != nil {
			return nil, err
		}
	}
	return r.Incumbent.Owners(ctx)
}

// TestWorkerCleanStopsOnMidSweepDisconnect proves the worker's clean-stop: a
// connection revoked mid-sweep makes reconcileConnection return
// ErrConnectionGone, and Work skips the workspace WITHOUT recording a backoff
// or a success — so the overlay_sync_state row teardown purged is not
// resurrected, and nothing is re-mirrored into the now-native workspace.
func TestWorkerCleanStopsOnMidSweepDisconnect(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})
	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "a@authz.test") // matches Rep1, so SeedUserMap reaches a fenced UpsertUserMap

	w := &overlayReconcileWorker{
		pool: e.Pool, vault: vault, ms: ms,
		meter: workerBudgetMeter(t),
		log:   slog.New(slog.DiscardHandler),
		newIncumbent: func(_, _ string) overlay.Incumbent {
			return &revokeOnOwnersIncumbent{Incumbent: fakeInc, pool: e.Pool}
		},
	}
	if err := w.Work(e.Admin(), nil); err != nil {
		t.Fatalf("Work must not error on a mid-sweep disconnect: %v", err)
	}

	// No sweep outcome was recorded: the clean-stop path skipped both
	// RecordSweepFailure and RecordSweepSuccess, so the purged
	// overlay_sync_state row stays gone (a resurrected row is exactly the P1
	// this fences).
	var syncStateRows, mirrorRows int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(e.Admin(), `SELECT count(*) FROM overlay_sync_state`).Scan(&syncStateRows); err != nil {
			return err
		}
		return tx.QueryRow(e.Admin(), `SELECT count(*) FROM overlay_mirror`).Scan(&mirrorRows)
	}); err != nil {
		t.Fatalf("counting post-sweep rows: %v", err)
	}
	if syncStateRows != 0 {
		t.Errorf("overlay_sync_state has %d row(s) after a mid-sweep disconnect, want 0 — the clean stop must not resurrect the purged backoff row", syncStateRows)
	}
	if mirrorRows != 0 {
		t.Errorf("overlay_mirror has %d row(s) after a mid-sweep disconnect, want 0 — nothing may be re-mirrored into a now-native workspace", mirrorRows)
	}
}

// TestOnDemandReconcileRacingDisconnectAnswersModeNotOverlay reproduces the
// real race a TTL-caching mode dispatcher opens: another process's
// Disconnect can already have committed (connection revoked,
// overlay_sync_state purged, workspace flipped to native) while THIS
// process is still serving a stale cached "overlay" read. After a genuine
// Connect + Disconnect, it restores ONLY workspace.x_sor_mode/x_incumbent
// via raw SQL — never incumbent_connection or overlay_sync_state, which
// stay exactly as the teardown left them — so requireOverlayMode passes
// and RequestSweep is forced through to the fenced write instead of being
// turned away earlier by the mode gate. This is the real regression guard
// for two failure modes that race exposes: (1) RequestSweep must run
// against the FENCED store (MirrorStore.WithFence), or this stale-mode
// window would let it silently re-insert the overlay_sync_state row the
// teardown purged; (2) the fence's ErrConnectionGone must be mapped to
// apperrors.ErrModeNotOverlay before it can cross the wire, or this
// answers an opaque 500 instead. Deleting either one independently fails
// this test.
func TestOnDemandReconcileRacingDisconnectAnswersModeNotOverlay(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})
	svc := overlay.NewService(e.Pool, vault, ms)
	adminCtx := overlayAdminCtx(e.WS, e.Rep1)

	if _, err := svc.Connect(adminCtx, overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := svc.Disconnect(adminCtx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	// Simulate the stale cached "overlay" mode read: restore ONLY
	// workspace.x_sor_mode/x_incumbent, leaving incumbent_connection
	// revoked and overlay_sync_state purged exactly as Disconnect left
	// them — so the mode gate passes and the call reaches the fence.
	if err := database.WithWorkspaceTx(adminCtx, e.Pool, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(adminCtx,
			`UPDATE workspace SET x_sor_mode = 'overlay', x_incumbent = 'hubspot'
			 WHERE id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`)
		return execErr
	}); err != nil {
		t.Fatalf("restoring the stale cached overlay mode: %v", err)
	}

	if err := svc.RequestSweep(adminCtx); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Fatalf("RequestSweep racing a disconnect = %v, want apperrors.ErrModeNotOverlay (not an opaque 500)", err)
	}

	var syncStateRows int
	if err := database.WithWorkspaceTx(adminCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(adminCtx, `SELECT count(*) FROM overlay_sync_state`).Scan(&syncStateRows)
	}); err != nil {
		t.Fatalf("counting overlay_sync_state rows: %v", err)
	}
	if syncStateRows != 0 {
		t.Errorf("overlay_sync_state has %d row(s) after a sweep request racing a disconnect, want 0 — the fence must not repopulate what the teardown purged", syncStateRows)
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
	meter := workerBudgetMeter(t)

	// First sweep mirrors the live record; the mapped reader can see it.
	if err := reconcileConnection(sweepCtx, e.Pool, vault, ms, meter, slog.New(slog.DiscardHandler), d, newInc); err != nil {
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
	if err := reconcileConnection(sweepCtx, e.Pool, vault, ms, meter, slog.New(slog.DiscardHandler), d, newInc); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if _, err := ms.Get(overlayReaderCtx(e.WS, e.Rep1), "person", "990009"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("Rep1 must NOT see the record after it was deleted incumbent-side, got: %v", err)
	}
}

// TestReconcileConnectionBackfillsTheWebhookPortalBinding proves the
// self-healing portal binding (OVA-DDL-3): a connection whose connect-time
// portal fetch was skipped/failed starts with a NULL incumbent_account_id (so a
// webhook for that portal resolves to nothing), and the next reconcile sweep
// fills it from the live adapter's account id — after which the portal binds to
// the workspace. This is the retry path that keeps a transient connect-time
// blip from permanently disabling webhook refresh.
func TestReconcileConnectionBackfillsTheWebhookPortalBinding(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})

	// Connect with NO incumbent factory: fetchPortalID is skipped, so the
	// binding starts NULL — the exact state the backfill exists to heal.
	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// Initially unbound: a webhook for the portal resolves to nothing.
	if _, err := overlay.WorkspaceForPortal(context.Background(), e.Pool, "hubspot", "portal-X"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("before backfill: WorkspaceForPortal = %v, want ErrNotFound (binding still null)", err)
	}

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

	fakeInc := fake.New()
	fakeInc.SeedAccountID("portal-X")
	sweepCtx := reconcileWorkerCtx(context.Background(), ids.From[ids.WorkspaceKind](e.WS))
	if err := reconcileConnection(sweepCtx, e.Pool, vault, ms, workerBudgetMeter(t),
		slog.New(slog.DiscardHandler), d, func(_, _ string) overlay.Incumbent { return fakeInc }); err != nil {
		t.Fatalf("reconcileConnection: %v", err)
	}

	// The sweep backfilled the binding: the portal now resolves to this workspace.
	got, err := overlay.WorkspaceForPortal(context.Background(), e.Pool, "hubspot", "portal-X")
	if err != nil {
		t.Fatalf("after backfill: WorkspaceForPortal(portal-X): %v", err)
	}
	if got.UUID != e.WS {
		t.Errorf("WorkspaceForPortal(portal-X) = %s, want the swept workspace %s", got.UUID, e.WS)
	}
}

// TestOverlayRefetchWorkerFreshensTheMirrorRecord is the webhook-signal
// re-fetch worker's real-Postgres proof (OVA-WIRE-10): given an active HubSpot
// overlay whose owner map the poller has already seeded, one Work call Gets a
// single record from the incumbent and ingests it through the same fenced,
// resolver-bound store the poller uses. So a first signal CREATES the mirror
// row a seed-mapped user can read; a fresher signal FRESHENS it in place; and
// an out-of-order (stale) signal is held back by the ingest staleness guard,
// never regressing the mirror. This is the receiver's payoff — a change in
// HubSpot reaches the mirror without waiting for the next poll.
func TestOverlayRefetchWorkerFreshensTheMirrorRecord(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})

	adminCtx := overlayAdminCtx(e.WS, e.Rep1)
	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(adminCtx, overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// The poller's sweep would have seeded the owner map from the directory;
	// seed it directly (through a directory-bound resolver, exactly as the
	// sweep does) so this test isolates the re-fetch worker's Get→Ingest rather
	// than re-exercising the backfill lane. owner-1's directory email is Rep1's
	// (a@authz.test — the shared harness seeds Rep1 as a@authz.test).
	dir := fake.New()
	dir.SeedOwner("owner-1", "a@authz.test")
	if err := ms.WithResolver(dir).SeedUserMap(adminCtx, incumbentHubSpot,
		[]overlay.OwnerRef{{ExternalID: "owner-1", Email: "a@authz.test"}}); err != nil {
		t.Fatalf("SeedUserMap: %v", err)
	}

	// version builds a fresh single-record incumbent for one signal. baseline
	// is set explicitly (never the wall clock) so the staleness ordering under
	// test is deterministic.
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	version := func(firstname string, modified time.Time) *fake.Adapter {
		inc := fake.New()
		// A live adapter resolves the record's owner email; the fake must too,
		// or ingest's owner-set revalidation would drop the seeded mapping as
		// unconfirmable (fail-closed) and hide the record.
		inc.SeedOwner("owner-1", "a@authz.test")
		rec := fake.Rec("c-1", map[string]any{"firstname": firstname})
		rec.ObjectClass = "person" // canonical — the mapping adapter's own translation, simulated
		rec.OwnerExternalID = "owner-1"
		rec.ModifiedAt = modified
		inc.Seed(overlay.IncumbentClassContacts, rec)
		return inc
	}
	// A HubSpot-configured meter so the refetch's ReserveREST (against the
	// connection's "hubspot" incumbent) is allowed rather than shed.
	restMeter := budgettest.Meter(t, budgettest.SmallConfig("hubspot"))
	runRefetch := func(inc *fake.Adapter) {
		t.Helper()
		w := &overlayRefetchWorker{
			pool: e.Pool, vault: vault, ms: ms, meter: restMeter,
			log:          slog.New(slog.DiscardHandler),
			newIncumbent: func(_, _ string) overlay.Incumbent { return inc },
		}
		if err := w.Work(context.Background(), &river.Job[OverlayRefetchArgs]{
			Args: OverlayRefetchArgs{Workspace: e.WS.String(), IncumbentClass: overlay.IncumbentClassContacts, ExternalID: "c-1"},
		}); err != nil {
			t.Fatalf("refetch Work: %v", err)
		}
	}
	firstname := func() any {
		t.Helper()
		row, err := ms.Get(overlayReaderCtx(e.WS, e.Rep1), "person", "c-1")
		if err != nil {
			t.Fatalf("Rep1 must see the re-fetched record: %v", err)
		}
		return row.Fields["firstname"]
	}

	// First signal: the record is not in the mirror yet — the worker creates
	// it, and the seed-mapped Rep1 can read it.
	runRefetch(version("Ada", base))
	if got := firstname(); got != "Ada" {
		t.Fatalf("firstname after first re-fetch = %v, want Ada", got)
	}

	// A fresher signal (newer baseline) freshens the same row in place.
	runRefetch(version("Ada Lovelace", base.Add(time.Minute)))
	if got := firstname(); got != "Ada Lovelace" {
		t.Fatalf("firstname after freshen = %v, want Ada Lovelace", got)
	}

	// A stale re-fetch (older baseline than the mirror already holds) is held
	// back by the ingest staleness guard — an out-of-order signal never
	// regresses the mirror.
	runRefetch(version("Stale", base))
	if got := firstname(); got != "Ada Lovelace" {
		t.Fatalf("a stale re-fetch must not regress the mirror; firstname = %v, want Ada Lovelace", got)
	}
}

// TestOverlayRefetchWorkerShedsWhenBudgetExhausted proves the OVB gate: when the
// meter sheds (here a fail-closed meter with no window), the worker skips the
// incumbent read entirely — nothing is ingested, and the poller heals the gap.
// This is the "never spend live quota we cannot account for" invariant for the
// webhook lane.
func TestOverlayRefetchWorkerShedsWhenBudgetExhausted(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})

	adminCtx := overlayAdminCtx(e.WS, e.Rep1)
	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(adminCtx, overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	dir := fake.New()
	dir.SeedOwner("owner-1", "a@authz.test")
	if err := ms.WithResolver(dir).SeedUserMap(adminCtx, incumbentHubSpot,
		[]overlay.OwnerRef{{ExternalID: "owner-1", Email: "a@authz.test"}}); err != nil {
		t.Fatalf("SeedUserMap: %v", err)
	}

	// The incumbent HAS the record — so a non-shed run would ingest it; the
	// record's absence afterward proves the shed skipped the read, not a miss.
	inc := fake.New()
	inc.SeedOwner("owner-1", "a@authz.test")
	rec := fake.Rec("c-1", map[string]any{"firstname": "Ada"})
	rec.ObjectClass = "person"
	rec.OwnerExternalID = "owner-1"
	rec.ModifiedAt = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	inc.Seed(overlay.IncumbentClassContacts, rec)

	// Spy on the incumbent so the test proves the live READ was skipped, not
	// merely that nothing was ingested (the reserve gates before inc.Get).
	spy := &getCountingIncumbent{Adapter: inc}
	// failClosedOverlayMeter sheds every reservation (no window to account into).
	w := &overlayRefetchWorker{
		pool: e.Pool, vault: vault, ms: ms, meter: failClosedOverlayMeter(),
		log:          slog.New(slog.DiscardHandler),
		newIncumbent: func(_, _ string) overlay.Incumbent { return spy },
	}
	if err := w.Work(context.Background(), &river.Job[OverlayRefetchArgs]{
		Args: OverlayRefetchArgs{Workspace: e.WS.String(), IncumbentClass: overlay.IncumbentClassContacts, ExternalID: "c-1"},
	}); err != nil {
		t.Fatalf("refetch Work (shed): %v", err)
	}
	// Shed → the incumbent read was never made (the whole point of the gate)...
	if spy.gets != 0 {
		t.Errorf("a shed re-fetch must not read the incumbent, got %d Get call(s)", spy.gets)
	}
	// ...and so nothing was mirrored (the poller heals later).
	if _, err := ms.Get(overlayReaderCtx(e.WS, e.Rep1), "person", "c-1"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("a shed re-fetch must ingest nothing, got: %v", err)
	}
}

// getCountingIncumbent counts Get calls so a test can assert the incumbent read
// was (not) made — e.g. that a budget shed skipped the read entirely.
type getCountingIncumbent struct {
	*fake.Adapter
	gets int
}

func (g *getCountingIncumbent) Get(ctx context.Context, objectClass, externalID string) (overlay.Record, error) {
	g.gets++
	return g.Adapter.Get(ctx, objectClass, externalID)
}
