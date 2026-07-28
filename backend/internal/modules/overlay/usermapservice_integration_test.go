// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// The admin user-map SERVICE against a real, migrated Postgres. The gate order
// (grant, human, overlay mode, fenced store) and the derivation of the page
// are unit-tested in handlers_usermap_test.go; what needs a database is
// everything that reads workspace.x_sor_mode, the mapping tables, or the
// disconnect fence — none of which can be faked without faking the very
// mechanism under test.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// directoryIncumbent serves a fixed owners directory (or a directory failure),
// reusing backfill_integration_test.go's pagingCompanies for the seam methods
// the user-map surface never calls — the overlay package cannot import
// overlay/fake without a self-import cycle.
type directoryIncumbent struct {
	pagingCompanies
	owners []OwnerRef
	err    error
}

var _ Incumbent = (*directoryIncumbent)(nil)

func (d *directoryIncumbent) Owners(context.Context) ([]OwnerRef, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.owners, nil
}

// connectedUserMapService builds a Service over an already-seeded workspace
// and connects it to inc — the state every user-map operation requires, since
// all four are refused outright in native mode. The vault round-trip the
// connect sets up is the path ownerDirectory actually takes to reach inc.
func connectedUserMapService(ctx context.Context, t *testing.T, pool *pgxpool.Pool, inc *directoryIncumbent) *Service {
	t.Helper()
	svc := NewService(pool, keyvault.NewMemory(), NewMirrorStore(pool, noOwnerEmails{})).
		WithIncumbentFactory(func(string, string) Incumbent { return inc })
	if _, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "pat-user-map"}); err != nil {
		t.Fatalf("connecting the fixture incumbent: %v", err)
	}
	return svc
}

// Native mode has no incumbent, so these operations have no equivalent — the
// /overlay cluster's own convention is a mode_not_overlay 404, never an
// empty page that would read as "this workspace has no users to map".
func TestUserMapSurfaceIs404InNativeMode(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	svc := NewService(pool, keyvault.NewMemory(), NewMirrorStore(pool, noOwnerEmails{})).
		WithIncumbentFactory(func(string, string) Incumbent { return &directoryIncumbent{} })
	someUser := ids.New[ids.UserKind]()

	if _, err := svc.UserMap(ctx, "", 50); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("UserMap in native mode = %v, want ErrModeNotOverlay", err)
	}
	if _, err := svc.Owners(ctx); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("Owners in native mode = %v, want ErrModeNotOverlay", err)
	}
	if err := svc.SetUserMap(ctx, someUser, "owner-1"); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("SetUserMap in native mode = %v, want ErrModeNotOverlay", err)
	}
	if err := svc.UnmapUser(ctx, someUser); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("UnmapUser in native mode = %v, want ErrModeNotOverlay", err)
	}
}

// disconnectUnderARequestHoldingAStaleMode runs the real teardown and then
// restores ONLY workspace.x_sor_mode/x_incumbent — everything the teardown
// purged stays purged, and the connection stays revoked.
//
// That reproduces the one interleaving the disconnect fence exists for, and
// the only one these service methods are actually exposed to: the mode gate
// and the store write run in SEPARATE transactions, so a Disconnect can commit
// entirely between them, leaving the in-flight request holding a mode value
// that says "overlay" against a workspace that no longer is one. Disconnecting
// BEFORE the call would not test the fence at all — resolveOverlayMode would
// refuse first and the write would never be reached, which is exactly why an
// unfenced store passes that weaker test.
func disconnectUnderARequestHoldingAStaleMode(ctx context.Context, t *testing.T, svc *Service, ws ids.UUID) {
	t.Helper()
	if err := svc.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if _, err := testOwnerConn(t).Exec(ctx,
		`UPDATE workspace SET x_sor_mode = 'overlay', x_incumbent = 'hubspot' WHERE id = $1`, ws); err != nil {
		t.Fatalf("restoring the mode the in-flight request already read: %v", err)
	}
}

// assertFence is a SILENT no-op on an unfenced store, and the composition layer
// hands this service a bare one — so a SetUserMap that forgets WithFence()
// looks correct and passes every test that does not race a disconnect, while
// inserting a mapping into a workspace whose teardown already purged the table
// and revoked its connection.
func TestSetUserMapRacingADisconnectIsRefused(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	svc := connectedUserMapService(ctx, t, pool, &directoryIncumbent{
		owners: []OwnerRef{{ExternalID: "owner-1", Email: "rep@acme.test"}},
	})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	disconnectUnderARequestHoldingAStaleMode(ctx, t, svc, ws)

	if err := svc.SetUserMap(ctx, rep, "owner-1"); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Fatalf("a mapping write racing a disconnect must be refused as mode_not_overlay, got: %v", err)
	}
	if got := countUserMapRows(ctx, t, pool, rep); got != 0 {
		t.Fatalf("no mapping may survive into a disconnected workspace, got %d", got)
	}
}

// The unmap path carries the same resurrection risk: its block row lands in a
// table teardown already purged, and the next connect would then start with an
// auto-map block nobody in that connection's lifetime ever set.
func TestUnmapUserRacingADisconnectIsRefused(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	svc := connectedUserMapService(ctx, t, pool, &directoryIncumbent{
		owners: []OwnerRef{{ExternalID: "owner-1", Email: "rep@acme.test"}},
	})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	disconnectUnderARequestHoldingAStaleMode(ctx, t, svc, ws)

	if err := svc.UnmapUser(ctx, rep); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Fatalf("an unmap racing a disconnect must be refused as mode_not_overlay, got: %v", err)
	}
	var blocks int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM mirror_user_automap_block WHERE app_user_id = $1`, rep).Scan(&blocks)
	}); err != nil {
		t.Fatalf("counting auto-map blocks: %v", err)
	}
	if blocks != 0 {
		t.Fatalf("no auto-map block may survive into a disconnected workspace, got %d", blocks)
	}
}

// Once the mode flip IS visible, the gate refuses every verb before the store
// is reached at all — the fence above is the backstop for the window this
// closes, not a replacement for it.
func TestUserMapSurfaceIs404AfterDisconnect(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	svc := connectedUserMapService(ctx, t, pool, &directoryIncumbent{})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	if err := svc.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if _, err := svc.UserMap(ctx, "", 50); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("UserMap after disconnect = %v, want ErrModeNotOverlay", err)
	}
	if err := svc.SetUserMap(ctx, rep, "owner-1"); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("SetUserMap after disconnect = %v, want ErrModeNotOverlay", err)
	}
}

// The write half end to end: the admin's pin lands as a manual override, and
// the page reads it back with the owner's identity resolved from the live
// directory — the two halves the settings card is built on.
func TestSetUserMapPinsAManualOverrideThePageReadsBack(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	svc := connectedUserMapService(ctx, t, pool, &directoryIncumbent{
		owners: []OwnerRef{{ExternalID: "owner-1", Email: "ada@acme.test", Name: "Ada Lovelace"}},
	})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	if err := svc.SetUserMap(ctx, rep, "owner-1"); err != nil {
		t.Fatalf("pinning the mapping: %v", err)
	}

	page, err := svc.UserMap(ctx, "", 50)
	if err != nil {
		t.Fatalf("reading the user map: %v", err)
	}
	if page.Incumbent != "hubspot" {
		t.Errorf("page incumbent = %q, want hubspot", page.Incumbent)
	}
	view, found := viewFor(page, rep)
	if !found {
		t.Fatal("the mapped user must appear on the page")
	}
	if view.IncumbentUserID != "owner-1" || view.MatchSource != "manual" {
		t.Errorf("mapping = %q/%q, want owner-1/manual", view.IncumbentUserID, view.MatchSource)
	}
	if view.OwnerName != "Ada Lovelace" || view.OwnerEmail != "ada@acme.test" {
		t.Errorf("owner identity = %q/%q, want Ada Lovelace/ada@acme.test", view.OwnerName, view.OwnerEmail)
	}
	if view.UnmappedReason != reasonNone || view.StaleOwnerRef {
		t.Errorf("a freshly pinned mapping must be clean: reason=%q stale=%v", view.UnmappedReason, view.StaleOwnerRef)
	}
}

// An empty owner reference names no incumbent user, and a mapping row carrying
// it would grant visibility over every mirrored record that has no owner at
// all. The transport answers 422 before reaching here; this is the backstop
// for every other caller.
func TestSetUserMapRefusesABlankIncumbentUserIDAndWritesNothing(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	svc := connectedUserMapService(ctx, t, pool, &directoryIncumbent{})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	if err := svc.SetUserMap(ctx, rep, ""); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("a blank incumbent user id must answer ErrNotFound, got: %v", err)
	}
	if got := countUserMapRows(ctx, t, pool, rep); got != 0 {
		t.Fatalf("a refused pin must write no mapping row, got %d", got)
	}
}

// The mapping table is the admin's only view of who is mapped, so a directory
// failure degrades the page rather than failing it — but it must never render
// a fabricated diagnosis from a directory it could not read.
func TestUserMapDegradesHonestlyWhenTheDirectoryCannotBeRead(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	svc := connectedUserMapService(ctx, t, pool, &directoryIncumbent{
		err: fmt.Errorf("test: the owners directory is unreachable"),
	})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)
	_, blockedRaw := testWorkspaceCtxAsUser(t, ws, "blocked@acme.test")
	blocked := ids.From[ids.UserKind](blockedRaw)
	if err := svc.UnmapUser(ctx, blocked); err != nil {
		t.Fatalf("recording the admin's block: %v", err)
	}

	page, err := svc.UserMap(ctx, "", 50)
	if err != nil {
		t.Fatalf("a directory failure must not fail the page: %v", err)
	}
	view, found := viewFor(page, rep)
	if !found {
		t.Fatal("the page must still list the workspace's users")
	}
	if view.UnmappedReason != reasonNoDirectory {
		t.Errorf("reason = %q, want %q — no reason is derivable without the directory", view.UnmappedReason, reasonNoDirectory)
	}
	// The block is this installation's own row, not a reading of the incumbent,
	// so an unreadable directory must not turn "you unmapped this person" into
	// "we could not look".
	blockedView, found := viewFor(page, blocked)
	if !found {
		t.Fatal("the blocked user must appear on the page")
	}
	if blockedView.UnmappedReason != reasonBlocked {
		t.Errorf("blocked user reason = %q, want %q", blockedView.UnmappedReason, reasonBlocked)
	}

	// Owners itself has nothing to degrade to: the picker would render an
	// empty directory as "this incumbent has no users", so the failure is
	// surfaced rather than swallowed into an empty list.
	if _, err := svc.Owners(ctx); err == nil {
		t.Fatal("an unreadable directory must fail the owners read, never answer an empty one")
	}
}

// A disconnect committing between the mode gate and the directory read is not
// a degraded directory — the workspace has no overlay at all — and answering a
// settled page would tell an admin their mapping table still governs records
// this installation no longer mirrors.
func TestUserMapAnswers404WhenTheConnectionVanishesBeforeTheDirectoryRead(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	svc := connectedUserMapService(ctx, t, pool, &directoryIncumbent{
		owners: []OwnerRef{{ExternalID: "owner-1", Email: "rep@acme.test"}},
	})
	testWorkspaceCtxAsUser(t, ws, "rep@acme.test")

	disconnectUnderARequestHoldingAStaleMode(ctx, t, svc, ws)

	if _, err := svc.UserMap(ctx, "", 50); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Fatalf("a page read racing a disconnect must answer mode_not_overlay, got: %v", err)
	}
}

// A directory the cap cut off is as unusable for the absence-based diagnoses as
// one that could not be read: absence from a list that stops at 500 is not
// absence from the incumbent. A block is not one of them — it is read out of
// our own tables — so it is still reported.
func TestUserMapDerivesNoAbsenceBasedReasonFromATruncatedDirectory(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	oversized := make([]OwnerRef, 0, ownerDirectoryCap+1)
	for i := range ownerDirectoryCap + 1 {
		oversized = append(oversized, OwnerRef{
			ExternalID: fmt.Sprintf("owner-%d", i),
			Email:      fmt.Sprintf("owner-%d@acme.test", i),
		})
	}
	svc := connectedUserMapService(ctx, t, pool, &directoryIncumbent{owners: oversized})
	_, unmatchedRaw := testWorkspaceCtxAsUser(t, ws, "unmatched@acme.test")
	unmatched := ids.From[ids.UserKind](unmatchedRaw)
	_, pinnedRaw := testWorkspaceCtxAsUser(t, ws, "pinned@acme.test")
	pinned := ids.From[ids.UserKind](pinnedRaw)
	// owner-0 is inside the cut-off, so this user's email DOES match a listed
	// owner: neither the match nor the truncation may displace the block.
	_, blockedRaw := testWorkspaceCtxAsUser(t, ws, "owner-0@acme.test")
	blocked := ids.From[ids.UserKind](blockedRaw)
	if err := svc.UnmapUser(ctx, blocked); err != nil {
		t.Fatalf("recording the admin's block: %v", err)
	}

	// Past the cut-off, so the loaded slice does not list this owner — which
	// says nothing about whether the incumbent still does.
	if err := svc.SetUserMap(ctx, pinned, fmt.Sprintf("owner-%d", ownerDirectoryCap)); err != nil {
		t.Fatalf("pinning the mapping: %v", err)
	}

	page, err := svc.UserMap(ctx, "", 50)
	if err != nil {
		t.Fatalf("reading the user map: %v", err)
	}
	unmatchedView, found := viewFor(page, unmatched)
	if !found {
		t.Fatal("the page must still list the workspace's users")
	}
	if unmatchedView.UnmappedReason != reasonNoDirectory {
		t.Errorf("reason = %q, want %q — a cut-off directory cannot prove no owner carries this email",
			unmatchedView.UnmappedReason, reasonNoDirectory)
	}
	pinnedView, found := viewFor(page, pinned)
	if !found {
		t.Fatal("the mapped user must appear on the page")
	}
	if pinnedView.StaleOwnerRef {
		t.Error("an owner past the cut-off must not be reported as gone from the incumbent's directory")
	}
	blockedView, found := viewFor(page, blocked)
	if !found {
		t.Fatal("the blocked user must appear on the page")
	}
	if blockedView.UnmappedReason != reasonBlocked {
		t.Errorf("blocked user reason = %q, want %q — a truncated directory does not unmake the admin's own decision",
			blockedView.UnmappedReason, reasonBlocked)
	}
}

// Owners() is unpaginated at the seam, so the cap here is the only bound on
// the response — and a capped list that did not say so would read as the
// incumbent's complete directory.
func TestOwnersCapsTheDirectoryAndReportsTheTruncation(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	oversized := make([]OwnerRef, 0, ownerDirectoryCap+1)
	for i := range ownerDirectoryCap + 1 {
		oversized = append(oversized, OwnerRef{
			ExternalID: fmt.Sprintf("owner-%d", i),
			Email:      fmt.Sprintf("owner-%d@acme.test", i),
		})
	}
	svc := connectedUserMapService(ctx, t, pool, &directoryIncumbent{owners: oversized})

	dir, err := svc.Owners(ctx)
	if err != nil {
		t.Fatalf("reading the owners directory: %v", err)
	}
	if len(dir.Owners) != ownerDirectoryCap {
		t.Errorf("directory carried %d owners, want the cap of %d", len(dir.Owners), ownerDirectoryCap)
	}
	if !dir.Truncated {
		t.Error("a capped directory must report truncated, never imply completeness")
	}
	if dir.Incumbent != "hubspot" {
		t.Errorf("directory incumbent = %q, want hubspot", dir.Incumbent)
	}
}

// A non-admin holding only the read grant every role carries must be refused
// by the real policy shape too, not just by the hand-built unit fixture.
func TestUserMapRefusesAMemberAgainstTheRealWorkspace(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	svc := connectedUserMapService(ctx, t, pool, &directoryIncumbent{})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	memberCtx := testMemberCtx(ws, repRaw)

	if _, err := svc.UserMap(memberCtx, "", 50); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("UserMap for a read-only member = %v, want ErrPermissionDenied", err)
	}
	if _, err := svc.Owners(memberCtx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Owners for a read-only member = %v, want ErrPermissionDenied", err)
	}
	if err := svc.UnmapUser(memberCtx, ids.From[ids.UserKind](repRaw)); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("UnmapUser for a read-only member = %v, want ErrPermissionDenied", err)
	}
}

// viewFor finds one user's row on a page.
func viewFor(page UserMapPage, user ids.UserID) (UserMapView, bool) {
	for _, v := range page.Entries {
		if v.AppUserID == user {
			return v, true
		}
	}
	return UserMapView{}, false
}

// countUserMapRows counts one user's mirror_user_map rows through the same
// workspace-scoped transaction the store writes them in.
func countUserMapRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool, user ids.UserID) int {
	t.Helper()
	var mapped int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM mirror_user_map WHERE app_user_id = $1`, user).Scan(&mapped)
	}); err != nil {
		t.Fatalf("counting mappings: %v", err)
	}
	return mapped
}
