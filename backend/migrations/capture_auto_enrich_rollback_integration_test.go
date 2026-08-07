// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// 0194's reverse carries data, and a reverse that carries data needs a test
// that carries data through it. The forward direction dropped
// workspace.capture_auto_enrich once the value lived in `setting`; the reverse
// re-adds the column and has to put the CURRENT value back — not the default,
// and not whatever the column held before the move.
//
// TestMigrations_applyReverseReapply cannot see this. It runs against a fresh
// schema, so the reverse's `UPDATE … FROM setting` matches nothing: a wrong
// cast, a wrong key string or a wrong join would all still pass. The precedent
// for testing a data-carrying reverse directly is
// comms_outbound_inflight_rollback_integration_test.go, and this follows it,
// including deriving the step count rather than writing a literal — a literal
// reverts whichever migration happens to be newest and quietly becomes a test
// of that one instead.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

func TestRollingBackTheAutoEnrichColumnRestoresTheCurrentPosture(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)
	ctx := context.Background()

	core, err := Core()
	if err != nil {
		t.Fatalf("loading core: %v", err)
	}
	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("up: %v", err)
	}
	seedWorkspace(t, conn, "auto-enrich-rollback")

	// An operator turned the spend control off AFTER the value moved, so the
	// setting row is the only record of it. This is the case the reverse
	// exists for: restoring the column from its own old contents would
	// resurrect the posture this write replaced.
	if _, err := conn.Exec(ctx, `
		INSERT INTO setting (key, value) VALUES ('capture.auto_enrich', 'false'::jsonb)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatalf("recording the operator's change: %v", err)
	}

	steps := stepsDownTo(t, core, "0194")
	if _, err := dbmigrate.Down(ctx, conn, core, steps); err != nil {
		t.Fatalf("reverting down to 0194: %v", err)
	}

	var autoEnrich bool
	if err := conn.QueryRow(ctx,
		`SELECT capture_auto_enrich FROM workspace WHERE archived_at IS NULL`).Scan(&autoEnrich); err != nil {
		t.Fatalf("reading the re-added column: %v", err)
	}
	if autoEnrich {
		t.Error("capture_auto_enrich = true after the rollback, want false — the reverse landed on the " +
			"column's default and threw away the posture an operator had switched off")
	}
}

// A setting row this build cannot decode must not take the whole rollback with
// it. `setting` carries no per-key type CHECK — the Go catalog constrains only
// what the store writes — so a hand-edited or externally seeded row is the
// realistic case, and a rollback that aborts on one is a rollback an operator
// cannot perform at the moment they most need it.
func TestRollingBackTolerAtesASettingValueThisBuildCannotRead(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)
	ctx := context.Background()

	core, err := Core()
	if err != nil {
		t.Fatalf("loading core: %v", err)
	}
	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("up: %v", err)
	}
	seedWorkspace(t, conn, "auto-enrich-rollback-bad-value")

	if _, err := conn.Exec(ctx, `
		INSERT INTO setting (key, value) VALUES ('capture.auto_enrich', '"off"'::jsonb)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatalf("seeding an undecodable value: %v", err)
	}

	steps := stepsDownTo(t, core, "0194")
	if _, err := dbmigrate.Down(ctx, conn, core, steps); err != nil {
		t.Fatalf("reverting down to 0194 with an undecodable setting: %v — a rollback must not "+
			"abort on a value it cannot read", err)
	}

	// It falls through to the re-added default, which is the posture a fresh
	// install ships with. Landing there is a decision, not an accident: the
	// alternative is refusing to roll back at all.
	var autoEnrich bool
	if err := conn.QueryRow(ctx,
		`SELECT capture_auto_enrich FROM workspace WHERE archived_at IS NULL`).Scan(&autoEnrich); err != nil {
		t.Fatalf("reading the re-added column: %v", err)
	}
	if !autoEnrich {
		t.Error("the undecodable value was somehow applied; it must fall through to the declared default")
	}
}
