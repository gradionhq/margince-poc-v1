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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
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
	if err := reconcileConnection(sweepCtx, vault, ms, workerBudgetMeter(t),
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
	err = reconcileConnection(sweepCtx, vault, ms, workerBudgetMeter(t),
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

// TestOnDemandReconcileRacingDisconnectAnswersModeNotOverlay proves the
// on-demand /overlay/reconcile boundary translates the disconnect-race
// sentinel into the same ErrModeNotOverlay a workspace with no active
// connection already gets — never an opaque 500.
func TestOnDemandReconcileRacingDisconnectAnswersModeNotOverlay(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})
	if _, err := overlay.NewService(e.Pool, vault, ms).
		Connect(overlayAdminCtx(e.WS, e.Rep1), overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "a@authz.test")

	r := overlayReconciler{
		pool: e.Pool, vault: vault, ms: ms,
		meter: workerBudgetMeter(t),
		log:   slog.New(slog.DiscardHandler),
		newIncumbent: func(_, _ string) overlay.Incumbent {
			return &revokeOnOwnersIncumbent{Incumbent: fakeInc, pool: e.Pool}
		},
	}
	if err := r.Reconcile(overlayAdminCtx(e.WS, e.Rep1)); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Fatalf("on-demand reconcile racing a disconnect = %v, want apperrors.ErrModeNotOverlay (not an opaque 500)", err)
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
