// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Migration 0149, proven against a real migrated Postgres: which of the
// reminders 0148 archived are given back, and which stay archived.
//
// 0148 archives every outstanding generated check-in task and justifies it
// with "the corrected scan re-mints the ones still deserved" — a claim that
// holds only where the reminder automation still runs. These suites pin the
// three answers that follow: a workspace that paused it gets its reminders
// back, a task somebody had taken over comes back whatever the workspace
// does, and a row 0148 never touched is left alone.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// 0148 archives every outstanding generated reminder on the reasoning that the
// corrected scan re-mints the ones that are still deserved. That holds only
// where the automation still runs, and 0148 never checked. 0149 gives back the
// rows nothing will bring back on its own.

// seedReminderAutomation inserts one clock automation in the given enabled
// state, so a suite can express "this workspace still runs reminders" and
// "this workspace paused them".
func seedReminderAutomation(t *testing.T, owner *pgx.Conn, ws ids.UUID, key string, enabled bool) {
	t.Helper()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO automation (id, workspace_id, key, name, trigger, action, params, enabled)
		 VALUES ($1, $2, $3, $3, '{"schedule":"clock"}', '{"kind":"create_task"}', '{}'::jsonb, $4)`,
		ids.NewV7(), ws, key, enabled); err != nil {
		t.Fatalf("seeding the %s automation (enabled=%v): %v", key, enabled, err)
	}
}

// seedUser inserts one workspace member, so a task can be assigned to a real
// row (assignee_id carries a foreign key).
func seedUser(t *testing.T, owner *pgx.Conn, ws ids.UUID) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name)
		 VALUES ($1, $2, $3, 'Reminder Owner')`,
		id, ws, fmt.Sprintf("owner-%s@restore.test", id)); err != nil {
		t.Fatalf("seeding the workspace member: %v", err)
	}
	return id
}

// assignTo makes a generated task somebody's own work, the way a person does
// by picking it up. The generated task carries no assignee, so this column
// having a value is what says a human took it over.
func assignTo(t *testing.T, owner *pgx.Conn, id, user ids.UUID) {
	t.Helper()
	if _, err := owner.Exec(context.Background(),
		`UPDATE activity SET assignee_id = $2 WHERE id = $1`, id, user); err != nil {
		t.Fatalf("assigning task %s: %v", id, err)
	}
}

// remindMeAt is the other mark of a person having adopted a task: they asked
// to be reminded about it. The generated task carries no remind_at either.
func remindMeAt(t *testing.T, owner *pgx.Conn, id ids.UUID, when time.Time) {
	t.Helper()
	if _, err := owner.Exec(context.Background(),
		`UPDATE activity SET remind_at = $2 WHERE id = $1`, id, when); err != nil {
		t.Fatalf("setting a reminder on task %s: %v", id, err)
	}
}

func TestTheRestoreGivesBackRemindersNothingWillReMint(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)

	// This workspace paused its reminders, so the corrected scan never runs
	// here and 0148's re-mint argument does not apply to a single row.
	seedReminderAutomation(t, owner, e.WS, "no_activity_reminder", false)
	stranded := seedTaskRow(t, owner, e.WS,
		"Check in — no activity since 2026-06-16", "system", false)
	strandedCadence := seedTaskRow(t, owner, e.WS,
		"Time for a check-in — last touched 2026-06-16", "system", false)

	applyCleanupMigration(t, owner)
	for _, id := range []ids.UUID{stranded, strandedCadence} {
		if !isArchived(t, owner, id) {
			t.Fatalf("0148 did not archive %s, so this suite is not testing what it claims", id)
		}
	}

	applyRestoreMigration(t, owner)
	for _, id := range []ids.UUID{stranded, strandedCadence} {
		if isArchived(t, owner, id) {
			t.Errorf("reminder %s stayed archived in a workspace whose automation is paused — nothing will ever mint it again", id)
		}
	}
}

func TestTheRestoreGivesBackAReminderSomebodyHadTakenOver(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)

	// Reminders DO still run here, so 0148's re-mint argument holds for an
	// untouched row — but a re-mint is a new row, without the assignee or the
	// date the person chose.
	seedReminderAutomation(t, owner, e.WS, "no_activity_reminder", true)
	assigned := seedTaskRow(t, owner, e.WS,
		"Check in — no activity since 2026-06-16", "system", false)
	reminded := seedTaskRow(t, owner, e.WS,
		"Check in — no activity since 2026-06-16", "system", false)
	untouched := seedTaskRow(t, owner, e.WS,
		"Check in — no activity since 2026-06-16", "system", false)
	assignTo(t, owner, assigned, seedUser(t, owner, e.WS))
	remindMeAt(t, owner, reminded, quietSince)

	applyCleanupMigration(t, owner)
	// Without this the assertions below pass vacuously: a row 0148 never
	// archived also reads "not archived" after the restore, and the suite
	// would be green while 0149 did nothing at all.
	for _, id := range []ids.UUID{assigned, reminded, untouched} {
		if !isArchived(t, owner, id) {
			t.Fatalf("0148 did not archive %s, so this suite is not testing what it claims", id)
		}
	}
	applyRestoreMigration(t, owner)

	if isArchived(t, owner, assigned) {
		t.Error("a reminder assigned to somebody stayed archived; the re-mint would not carry the assignee")
	}
	if isArchived(t, owner, reminded) {
		t.Error("a reminder somebody set a reminder on stayed archived; the re-mint would not carry the date they chose")
	}
	// The whole point of 0148 stands for a row the scan will genuinely
	// reconsider: it stays archived rather than coming back twice.
	if !isArchived(t, owner, untouched) {
		t.Error("an untouched reminder in a live workspace came back, so the corrected scan will mint a duplicate beside it")
	}
}

// A reminder archived BEFORE 0148 is not 0148's to give back. 0148 only
// touched rows that were still live (archived_at IS NULL), so it never saw
// this one — whoever archived it, for whatever reason, meant it.
//
// This is why the ledger instant is the discriminator and the audit log is
// not: a row archived earlier by any path that wrote no audit entry is
// indistinguishable from 0148's own work under an audit-based test.
func TestTheRestoreLeavesARowArchivedBefore0148Alone(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)

	// Paused, so the "nothing will re-mint it" arm is satisfied and only the
	// provenance check can keep this row archived.
	seedReminderAutomation(t, owner, e.WS, "no_activity_reminder", false)
	earlier := seedTaskRow(t, owner, e.WS,
		"Check in — no activity since 2026-06-16", "system", false)
	if _, err := owner.Exec(context.Background(),
		`UPDATE activity SET archived_at = now() - interval '1 day' WHERE id = $1`,
		earlier); err != nil {
		t.Fatalf("archiving the task ahead of 0148: %v", err)
	}

	// 0148 runs now and skips it — it is already archived.
	applyCleanupMigration(t, owner)
	applyRestoreMigration(t, owner)

	if !isArchived(t, owner, earlier) {
		t.Error("a reminder archived before 0148 ran was handed back; 0148 never took it away")
	}
}

func TestTheRestorePairsEachReminderWithItsOwnAutomation(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)

	seedReminderAutomation(t, owner, e.WS, "no_activity_reminder", true)
	seedReminderAutomation(t, owner, e.WS, "check_in_cadence", false)
	live := seedTaskRow(t, owner, e.WS,
		"Check in — no activity since 2026-06-16", "system", false)
	stranded := seedTaskRow(t, owner, e.WS,
		"Time for a check-in — last touched 2026-06-16", "system", false)

	applyCleanupMigration(t, owner)
	// Same guard as its siblings: "stranded came back" only means anything
	// once 0148 has been shown to have taken it away.
	for _, id := range []ids.UUID{live, stranded} {
		if !isArchived(t, owner, id) {
			t.Fatalf("0148 did not archive %s, so this suite is not testing what it claims", id)
		}
	}
	applyRestoreMigration(t, owner)

	if !isArchived(t, owner, live) {
		t.Error("a reminder whose own automation still runs came back; the scan will mint a duplicate beside it")
	}
	if isArchived(t, owner, stranded) {
		t.Error("a cadence reminder stayed archived while only the OTHER automation runs — nothing will ever mint it again")
	}
}
