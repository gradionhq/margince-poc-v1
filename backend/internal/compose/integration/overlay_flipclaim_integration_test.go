// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The flip's claim and the liveness signal Disconnect reads off it,
// against real Postgres advisory locks.
//
// These two must agree: the claim is what serializes a cutover, and
// FlipImportProbe is what stops a disconnect from purging the mirror
// under a running import. Nothing but this lane proves they key on the
// same lock — the overlay module's own suite injects a fake probe, so a
// divergence there would pass every other test while quietly either
// latching Disconnect shut or letting it race an import.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// takeFlipClaim takes the real advisory claim and releases it at test
// end; the release is idempotent, so a case may also call it mid-test
// to prove what the lock's absence changes.
func takeFlipClaim(ctx context.Context, t *testing.T, pool *pgxpool.Pool) func() {
	t.Helper()
	release, err := compose.ClaimFlipForTest(ctx, pool)
	if err != nil {
		t.Fatalf("claiming the flip: %v", err)
	}
	t.Cleanup(release)
	return release
}

// flipLiveness reads the probe Disconnect consults, in f's workspace.
func (f flipEstate) flipLiveness(t *testing.T) bool {
	t.Helper()
	var live bool
	f.inWorkspaceTx(t, func(tx pgx.Tx) error {
		var err error
		live, err = compose.FlipImportProbe(f.adminCtx, tx)
		return err
	})
	return live
}

// startMirrorRun records a running mirror-connector run, the half of
// the probe that survives a crash.
func (f flipEstate) startMirrorRun(t *testing.T, snapshot string) {
	t.Helper()
	if _, err := migration.NewRunStore(f.pool).Create(f.adminCtx, migration.CreateRunInput{
		Connector: migration.ConnectorMirror, SourceRef: snapshot, Source: "overlay:flip",
	}); err != nil {
		t.Fatalf("creating the mirror run: %v", err)
	}
}

func TestFlipClaimAndLivenessProbeAgree(t *testing.T) {
	f := setupFlipEstate(t)
	runInFlight := func() bool {
		t.Helper()
		var running bool
		f.inWorkspaceTx(t, func(tx pgx.Tx) error {
			var err error
			running, err = migration.MirrorRunInFlight(f.adminCtx, tx)
			return err
		})
		return running
	}

	if f.flipLiveness(t) || runInFlight() {
		t.Fatal("an idle workspace reported a live flip import")
	}

	// A held claim alone is NOT liveness: a preflight's parity dry-run
	// holds the same lock for minutes, and refusing Disconnect for a
	// readiness check is the latch the probe exists to avoid.
	release := takeFlipClaim(f.adminCtx, t, f.pool)
	if f.flipLiveness(t) {
		t.Error("a held claim with no running run reported as a live import — a preflight would latch Disconnect shut")
	}

	// Claim + a running mirror run: an import actually moving, and the
	// only state Disconnect refuses on.
	f.startMirrorRun(t, "snap-claim-test")
	if !runInFlight() {
		t.Fatal("a running mirror run was not seen")
	}
	if !f.flipLiveness(t) {
		t.Fatal("claim + running run must read as a live import — otherwise Disconnect races the flip")
	}

	// Releasing the claim clears liveness even though the run row still
	// says running: that stale row is exactly what a cancelled request
	// leaves behind, and trusting it alone would block the only path
	// that revokes the incumbent credential.
	release()
	if !runInFlight() {
		t.Fatal("the run row should still say running — that is the stale state the lock protects against")
	}
	if f.flipLiveness(t) {
		t.Error("an abandoned run (lock gone) still reported live; Disconnect would be latched shut permanently")
	}
}

// One workspace's flip must not make another's disconnect refuse. Both
// halves of the probe are workspace-derived — the advisory key from the
// workspace id, the run row through RLS — and this pins the KEY half:
// the run row is this workspace's, so only the lock is under test, and
// a claim taken under a different workspace id must not register here.
func TestFlipLivenessIsKeyedOnTheWorkspace(t *testing.T) {
	f := setupFlipEstate(t)
	f.startMirrorRun(t, "snap-scope-test")

	// A claim held under a FOREIGN workspace id, in the same database.
	takeFlipClaim(flipAdminCtx(ids.NewV7(), f.adminID), t, f.pool)
	if f.flipLiveness(t) {
		t.Error("another workspace's claim made this one look busy — its disconnect would refuse for a flip it has nothing to do with")
	}

	// Positive control: this workspace's OWN claim does register, so the
	// negative above is a scoping result rather than a probe that never
	// answers true.
	takeFlipClaim(f.adminCtx, t, f.pool)
	if !f.flipLiveness(t) {
		t.Error("this workspace's own claim + running run must read as live")
	}
}
