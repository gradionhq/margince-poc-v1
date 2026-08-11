// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// 0216 gives an installation that already booted the agent seat bootstrap now
// writes for a new one. Nothing else in the lane can prove it works.
//
// The core-lane replay applies it against a FRESHLY reset schema, where no
// workspace exists — so the backfill runs, writes nothing, and reports success,
// which is indistinguishable from the failure it must not have. The static
// gate reads its shape and cannot execute it. What is left, and what this file
// supplies, is a database that holds workspaces when the statement runs, under
// both roles that change its meaning:
//
//   - as a SUPERUSER, row-level security does not filter, so the statement's own
//     workspace predicate is the only thing scoping it. Without that predicate
//     every iteration reaches every workspace and the second one collides on the
//     seat address.
//   - as the NON-SUPERUSER migration role a deployed installation actually uses,
//     FORCE row-level security binds, and the per-iteration binding is the only
//     thing that lets the INSERT's WITH CHECK pass at all.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestTheAgentSeatBackfillGivesEverySeatlessWorkspaceExactlyOneSeat(t *testing.T) {
	ctx := context.Background()
	admin := connect(t, mustOwnerDSN(t))
	resetSchema(t, admin)
	migrator := asMigrator(t, admin)
	migrateAll(t, migrator)
	backfill := agentSeatBackfillSQL(t)

	// Seeded AFTER the lane: an installation that existed before this migration
	// was written, which is the only case the backfill is for.
	first := seedWorkspace(t, admin, "backfill-one")
	second := seedWorkspace(t, admin, "backfill-two")

	// The superuser pass. It reaches every workspace's rows on every iteration,
	// so a statement that did not name its own workspace would write both seats
	// twice and fail on the case-insensitive email index the second time.
	if _, err := admin.Exec(ctx, backfill); err != nil {
		t.Fatalf("the backfill failed under a superuser owner, which row-level security does not "+
			"filter — so this is the statement itself, not the policy: %v", err)
	}
	assertOneAgentSeat(ctx, t, admin, first, "agent@backfill-one.gradion.local")
	assertOneAgentSeat(ctx, t, admin, second, "agent@backfill-two.gradion.local")

	// A third workspace, and a second pass as the role a deployed installation
	// migrates with. Two things at once: the new workspace gets its seat — which
	// only happens if the binding made WITH CHECK pass — and the two that
	// already hold one do not acquire a second, which is what makes the
	// migration safe to re-run and what protects an installation that inserted
	// its own seat by hand.
	third := seedWorkspace(t, admin, "backfill-three")
	if _, err := migrator.Exec(ctx, backfill); err != nil {
		t.Fatalf("the backfill failed under the non-superuser migration role. Every deployed "+
			"installation migrates as this role, so an unbound INSERT is refused here and nowhere "+
			"a developer would see it: %v", err)
	}
	assertOneAgentSeat(ctx, t, admin, third, "agent@backfill-three.gradion.local")
	assertOneAgentSeat(ctx, t, admin, first, "agent@backfill-one.gradion.local")
	assertOneAgentSeat(ctx, t, admin, second, "agent@backfill-two.gradion.local")
}

// assertOneAgentSeat reads the workspace's agent seats through the admin
// connection, naming the workspace explicitly: that connection is a superuser,
// so an unqualified read answers with every workspace's rows and a seat written
// to the wrong tenant would still be found.
func assertOneAgentSeat(ctx context.Context, t *testing.T, admin *pgx.Conn, wsID, wantEmail string) {
	t.Helper()
	rows, err := admin.Query(ctx,
		`SELECT email, display_name, status, seat_type, password_hash IS NOT NULL
		   FROM app_user WHERE workspace_id = $1 AND is_agent`, wsID)
	if err != nil {
		t.Fatalf("reading the agent seats of %s: %v", wantEmail, err)
	}
	type seat struct {
		email, displayName, status, seatType string
		hasPassword                          bool
	}
	seats, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (seat, error) {
		var s seat
		return s, r.Scan(&s.email, &s.displayName, &s.status, &s.seatType, &s.hasPassword)
	})
	if err != nil {
		t.Fatalf("collecting the agent seats of %s: %v", wantEmail, err)
	}
	if len(seats) != 1 {
		t.Fatalf("workspace %s holds %d agent seat(s), want exactly 1 — none leaves every scheduled "+
			"job skipped, and more than one makes which seat initiates them arbitrary", wsID, len(seats))
	}
	got := seats[0]
	if got.email != wantEmail {
		t.Errorf("seat address = %q, want %q — the address is derived per workspace, so a shared one "+
			"means the loop wrote one workspace's row for another", got.email, wantEmail)
	}
	if got.displayName != "Gradion Agent" || got.status != "active" || got.seatType != "full" {
		t.Errorf("seat = (%q, %q, %q), want (\"Gradion Agent\", \"active\", \"full\") — the dispatcher "+
			"resolves an initiator by `is_agent AND status = 'active'`, and the schema admits no seat "+
			"type but full for an agent", got.displayName, got.status, got.seatType)
	}
	if got.hasPassword {
		t.Error("the backfilled seat carries a password hash, which makes an identity with no person " +
			"behind it a credential somebody must administer")
	}
}

// agentSeatBackfillSQL returns the migration's own text. Reading it out of the
// embedded namespace rather than restating it here is the point: a test that
// carried its own copy of the statement would keep passing after the shipped one
// changed.
func agentSeatBackfillSQL(t *testing.T) string {
	t.Helper()
	core, err := Core()
	if err != nil {
		t.Fatalf("loading core migrations: %v", err)
	}
	for _, migration := range core.Migrations {
		if migration.Name == "agent_seat_backfill" {
			return migration.UpSQL
		}
	}
	t.Fatal("the core namespace holds no agent_seat_backfill migration — it was renamed or removed, " +
		"and this test would otherwise report an absent backfill as a working one")
	return ""
}
