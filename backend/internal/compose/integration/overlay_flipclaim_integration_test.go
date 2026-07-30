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
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

func TestFlipClaimAndLivenessProbeAgree(t *testing.T) {
	f := setupFlipEstate(t)

	probe := func() bool {
		t.Helper()
		var live bool
		f.inWorkspaceTx(t, func(tx pgx.Tx) error {
			var err error
			live, err = compose.FlipImportProbe(f.adminCtx, tx)
			return err
		})
		return live
	}
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

	// Idle workspace: no claim, no run.
	if probe() {
		t.Fatal("an idle workspace reported a live flip import")
	}
	if runInFlight() {
		t.Fatal("an idle workspace has no mirror run")
	}

	// A held claim alone is NOT liveness: a preflight's parity dry-run
	// holds the same lock for minutes, and refusing Disconnect for a
	// readiness check is the latch the probe exists to avoid.
	release := compose.ClaimFlipForTest(f.adminCtx, t, f.pool)
	if probe() {
		t.Error("a held claim with no running run reported as a live import — a preflight would latch Disconnect shut")
	}

	// Claim + a running mirror run: that is an import actually moving,
	// and the only state Disconnect refuses on.
	runs := migration.NewRunStore(f.pool)
	run, err := runs.Create(f.adminCtx, migration.CreateRunInput{
		Connector: migration.ConnectorMirror, SourceRef: "snap-claim-test", Source: "overlay:flip",
	})
	if err != nil {
		t.Fatalf("creating the mirror run: %v", err)
	}
	if !runInFlight() {
		t.Fatal("a running mirror run was not seen")
	}
	if !probe() {
		t.Fatal("claim + running run must read as a live import — otherwise Disconnect races the flip")
	}

	// Releasing the claim is enough to clear liveness even though the
	// run row still says running: a cancelled request leaves that row
	// behind forever, and trusting it alone would block the only path
	// that revokes the incumbent credential.
	release()
	if !runInFlight() {
		t.Fatal("the run row should still say running — that is the stale state the lock protects against")
	}
	if probe() {
		t.Error("an abandoned run (lock gone) still reported live; Disconnect would be latched shut permanently")
	}
	_ = run
}

// livenessProbeIsWorkspaceScoped: one workspace's claim must not make
// another's disconnect refuse.
func TestFlipLivenessIsWorkspaceScoped(t *testing.T) {
	a := setupFlipEstate(t)
	release := compose.ClaimFlipForTest(a.adminCtx, t, a.pool)
	defer release()

	runs := migration.NewRunStore(a.pool)
	if _, err := runs.Create(a.adminCtx, migration.CreateRunInput{
		Connector: migration.ConnectorMirror, SourceRef: "snap-a", Source: "overlay:flip",
	}); err != nil {
		t.Fatalf("creating workspace A's run: %v", err)
	}

	b := setupFlipEstate(t)
	var live bool
	if err := database.WithWorkspaceTx(b.adminCtx, b.pool, func(tx pgx.Tx) error {
		var err error
		live, err = compose.FlipImportProbe(b.adminCtx, tx)
		return err
	}); err != nil {
		t.Fatalf("probing workspace B: %v", err)
	}
	if live {
		t.Error("workspace A's running flip made workspace B look busy — B's disconnect would refuse for a flip it has nothing to do with")
	}
}
