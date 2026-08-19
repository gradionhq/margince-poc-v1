// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// 1787121028 undoes damage 1787111736 did, so its test has to carry that damage
// through it. Applied to a fresh schema the repair matches nothing — there is
// no flag to restore — and a wrong predicate, a wrong direction of the date
// comparison, or no repair at all would every one of them pass.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

// noticeRow is one seeded employment and whether the repair should hand its
// current-primary flag back.
type noticeRow struct {
	label       string
	person      string
	org         string
	endsInDays  int // 0 = no end date at all
	primary     bool
	endsPrimary bool
}

func seedNoticePeriods(t *testing.T, conn *pgx.Conn, rows []noticeRow) map[string]string {
	t.Helper()
	ctx := context.Background()
	people, orgs := map[string]string{}, map[string]string{}
	for _, row := range rows {
		for _, pair := range []struct {
			cache map[string]string
			name  string
			stmt  string
		}{
			{people, row.person, `INSERT INTO person (full_name, source, captured_by) VALUES ($1, 'test', 'human:test') RETURNING id`},
			{orgs, row.org, `INSERT INTO organization (display_name, source, captured_by) VALUES ($1, 'test', 'human:test') RETURNING id`},
		} {
			if _, seeded := pair.cache[pair.name]; seeded {
				continue
			}
			var id string
			if err := conn.QueryRow(ctx, pair.stmt, pair.name).Scan(&id); err != nil {
				t.Fatalf("seeding %q: %v", pair.name, err)
			}
			pair.cache[pair.name] = id
		}
	}
	ids := map[string]string{}
	for _, row := range rows {
		var id string
		// The date is computed by the DATABASE, from the same clock the
		// migration's own current_date reads. A date built in Go would drift
		// from it by a day whenever the two disagree about midnight.
		if err := conn.QueryRow(ctx, `
			INSERT INTO relationship (kind, person_id, organization_id, is_current_primary, ended_at, source, captured_by)
			VALUES ('employment', $1, $2, $3,
			        CASE WHEN $4::int = 0 THEN NULL ELSE current_date + $4::int END,
			        'test', 'human:test')
			RETURNING id`,
			people[row.person], orgs[row.org], row.primary, row.endsInDays).Scan(&id); err != nil {
			t.Fatalf("seeding employment %q: %v", row.label, err)
		}
		ids[row.label] = id
	}
	return ids
}

func TestTheNoticePeriodRepairGivesBackOnlyTheFlagsItShould(t *testing.T) {
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
	// Back to the schema this repair lands on, so the rows below are the rows
	// 1787111736 left behind on a database that had already applied it.
	if _, err := dbmigrate.Down(ctx, conn, core, stepsDownTo(t, core, "1787121028")); err != nil {
		t.Fatalf("reverting down to 1787121028: %v", err)
	}

	rows := []noticeRow{
		// The damage: a last day on file, the flag stripped, and it is the only
		// job they have. This is the person the repair exists for.
		{label: "serving notice", person: "leaver", org: "employer", endsInDays: 90, endsPrimary: true},

		// Already gone. 1787111736 was RIGHT about this one.
		{label: "actually left", person: "alum", org: "employer", endsInDays: -30, endsPrimary: false},

		// Never touched by 1787111736 — no end date, so no flag was taken. The
		// repair must not invent one where nothing was lost.
		{label: "no end date", person: "staying", org: "employer", endsPrimary: false},

		// Serving notice at one company and already primary at another: the
		// flag is not free to give, and uq_rel_current_primary_employer would
		// refuse it anyway.
		{label: "notice, has another", person: "moving", org: "employer", endsInDays: 60, endsPrimary: false},
		{label: "the new job", person: "moving", org: "other", primary: true, endsPrimary: true},

		// Two notices and no flag: which one wins is not this migration's
		// question (#1781), so it answers neither.
		{label: "two notices A", person: "undecided", org: "employer", endsInDays: 30, endsPrimary: false},
		{label: "two notices B", person: "undecided", org: "other", endsInDays: 45, endsPrimary: false},
	}
	seeded := seedNoticePeriods(t, conn, rows)

	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("applying 1787121028: %v", err)
	}

	for _, row := range rows {
		var primary bool
		if err := conn.QueryRow(ctx,
			`SELECT is_current_primary FROM relationship WHERE id = $1`, seeded[row.label]).Scan(&primary); err != nil {
			t.Fatalf("reading back %q: %v", row.label, err)
		}
		if primary != row.endsPrimary {
			t.Errorf("%q current primary = %t, want %t", row.label, primary, row.endsPrimary)
		}
	}
}
