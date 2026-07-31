// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Which quiet records the clock scan is allowed to remind about, proven
// against a real migrated Postgres. The scan's candidate query
// (activities/lasttouch.go's LastTouchBefore) answers two questions at
// once — has this record gone quiet, and is anyone actually working it —
// and each suite below pins one arm of the second question. Quietness
// itself, the occurrence key, and the re-arm on a fresh touch are proven
// next door (timescan_integration_test.go).
//
// The clock is pinned (NewTimeScannerWithClock) so "no activity for N
// days" is evaluated against seeded timestamps, never the wall clock; no
// sleep, no real-time flakiness. Rows created through the module stores
// carry a real now() creation stamp, so every fixture that must predate
// the cutoff is shifted explicitly through the owner connection —
// otherwise the creation grace (a record younger than the cutoff is not
// stale, however old its imported activities are) would hide the very
// behaviour under test.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/migrations"
)

// eligibilityScanNow is the instant every suite in this file evaluates
// against. With the seeded automation carrying no params, the threshold is
// the 7-day default, so the cutoff is eligibilityScanNow-7d.
var eligibilityScanNow = time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)

// quietSince is comfortably past that cutoff — the last genuine touch of
// every fixture below.
var quietSince = eligibilityScanNow.AddDate(0, 0, -30)

// longEstablished is a creation stamp well before the cutoff, so the
// creation grace never decides a test that is about something else.
var longEstablished = eligibilityScanNow.AddDate(0, 0, -60)

func TestQuietOrganizationWithNoOpenDealIsNotRemindedAbout(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)

	org := e.SeedOrg(t, "Dormant Account", nil)
	backdateCreatedAt(t, owner, "organization", org, longEstablished)
	linkQuietTouch(t, owner, e.WS, "organization", org)
	seedNoActivityReminder(t, owner, e.WS)

	runEligibilityScan(t, e)

	if got := taskCountOn(t, e, "organization", org); got != 0 {
		t.Fatalf("reminder tasks on an account with no open deal = %d, want 0 — nobody is working this company", got)
	}
	if got := runCountForHandler(t, e, "no_activity_reminder"); got != 0 {
		t.Fatalf("workflow_run rows = %d, want 0 — an ineligible record must never reach the batch at all", got)
	}
}

func TestQuietOrganizationWithAnOpenDealFiresOnceAndStaysClaimed(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	pipeline, open, _ := DealFixture(t, e)

	org := e.SeedOrg(t, "Live Account", nil)
	deal := e.SeedDeal(t, "Live Account Renewal", pipeline, open, nil)
	attachDealToOrg(t, owner, deal, org)
	backdateCreatedAt(t, owner, "organization", org, longEstablished)
	backdateCreatedAt(t, owner, "deal", deal, longEstablished)
	// Linked to the ACCOUNT only, so this suite is about the organization
	// arm of the eligibility rule and not about the deal's own arm.
	linkQuietTouch(t, owner, e.WS, "organization", org)
	seedNoActivityReminder(t, owner, e.WS)

	runEligibilityScan(t, e)
	if got := taskCountOn(t, e, "organization", org); got != 1 {
		t.Fatalf("reminder tasks after the first pass = %d, want exactly 1 — an account with an open deal is worth a reminder", got)
	}

	runEligibilityScan(t, e)
	if got := taskCountOn(t, e, "organization", org); got != 1 {
		t.Fatalf("reminder tasks after the second pass = %d, want still exactly 1 — the unchanged anchor must hold its claim", got)
	}
}

func TestTwoRecordsSharingOneLastTouchInstantEachGetTheirOwnReminder(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	pipeline, open, _ := DealFixture(t, e)

	org := e.SeedOrg(t, "Shared Anchor Account", nil)
	deal := e.SeedDeal(t, "Shared Anchor Deal", pipeline, open, nil)
	attachDealToOrg(t, owner, deal, org)
	person := e.SeedPerson(t, "Champion", nil)
	seedStakeholderSeat(t, owner, e.WS, person, deal)
	for _, row := range []struct {
		table string
		id    ids.UUID
	}{{"organization", org}, {"deal", deal}, {"person", person}} {
		backdateCreatedAt(t, owner, row.table, row.id, longEstablished)
	}

	// ONE captured mail on both timelines — the account's and its
	// champion's — which is exactly how two records come to share a single
	// last-touch instant.
	touch := seedQuietTouch(t, owner, e.WS)
	linkTouch(t, owner, e.WS, touch, "organization", org)
	linkTouch(t, owner, e.WS, touch, "person", person)
	seedNoActivityReminder(t, owner, e.WS)

	runEligibilityScan(t, e)

	if got := taskCountOn(t, e, "organization", org); got != 1 {
		t.Errorf("reminder tasks on the account = %d, want exactly 1", got)
	}
	if got := taskCountOn(t, e, "person", person); got != 1 {
		t.Errorf("reminder tasks on the champion = %d, want exactly 1 — one record's claim must not absorb the other's", got)
	}
	if got := runCountForHandler(t, e, "no_activity_reminder"); got != 2 {
		t.Errorf("workflow_run rows = %d, want 2 — one claim per record, not one per anchor instant", got)
	}
}

func TestARecordYoungerThanTheCutoffIsNotStale(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	pipeline, open, _ := DealFixture(t, e)

	org := e.SeedOrg(t, "Imported Yesterday", nil)
	deal := e.SeedDeal(t, "Imported Yesterday Deal", pipeline, open, nil)
	attachDealToOrg(t, owner, deal, org)
	backdateCreatedAt(t, owner, "deal", deal, longEstablished)
	// The account itself arrived one day before the scan; the mail history
	// imported with it is weeks old. Age of the CONTENT is not neglect.
	backdateCreatedAt(t, owner, "organization", org, eligibilityScanNow.AddDate(0, 0, -1))
	linkQuietTouch(t, owner, e.WS, "organization", org)
	seedNoActivityReminder(t, owner, e.WS)

	runEligibilityScan(t, e)

	if got := taskCountOn(t, e, "organization", org); got != 0 {
		t.Fatalf("reminder tasks on an account created yesterday = %d, want 0 — backfilled history is not a quiet spell", got)
	}
}

func TestOnlyAStakeholderSeatMakesAPersonACandidate(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	pipeline, open, _ := DealFixture(t, e)

	org := e.SeedOrg(t, "Busy Account", nil)
	deal := e.SeedDeal(t, "Busy Account Deal", pipeline, open, nil)
	attachDealToOrg(t, owner, deal, org)
	stakeholder := e.SeedPerson(t, "Champion", nil)
	seedStakeholderSeat(t, owner, e.WS, stakeholder, deal)
	// An employee of the same busy account with no seat on the deal. If
	// employment alone made a candidate, every colleague would earn a
	// reminder duplicating the account's own.
	colleague := e.SeedPerson(t, "Colleague", nil)
	seedEmployment(t, owner, e.WS, colleague, org)
	for _, row := range []struct {
		table string
		id    ids.UUID
	}{{"organization", org}, {"deal", deal}, {"person", stakeholder}, {"person", colleague}} {
		backdateCreatedAt(t, owner, row.table, row.id, longEstablished)
	}
	linkQuietTouch(t, owner, e.WS, "person", stakeholder)
	linkQuietTouch(t, owner, e.WS, "person", colleague)
	seedNoActivityReminder(t, owner, e.WS)

	runEligibilityScan(t, e)

	if got := taskCountOn(t, e, "person", stakeholder); got != 1 {
		t.Errorf("reminder tasks on the deal's champion = %d, want exactly 1", got)
	}
	if got := taskCountOn(t, e, "person", colleague); got != 0 {
		t.Errorf("reminder tasks on a colleague with no seat on the deal = %d, want 0", got)
	}
}

func TestOnlyALeadStillInPlayIsACandidate(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)

	working := seedLeadInStatus(t, owner, e.WS, "working")
	disqualified := seedLeadInStatus(t, owner, e.WS, "disqualified")
	for _, id := range []ids.UUID{working, disqualified} {
		backdateCreatedAt(t, owner, "lead", id, longEstablished)
		linkQuietTouch(t, owner, e.WS, "lead", id)
	}
	seedNoActivityReminder(t, owner, e.WS)

	runEligibilityScan(t, e)

	if got := taskCountOn(t, e, "lead", working); got != 1 {
		t.Errorf("reminder tasks on a lead still being worked = %d, want exactly 1", got)
	}
	if got := taskCountOn(t, e, "lead", disqualified); got != 0 {
		t.Errorf("reminder tasks on a disqualified lead = %d, want 0 — that lead is finished business", got)
	}
}

// TestCleanupMigrationArchivesGeneratedRemindersOnly runs the shipped
// migration SQL itself, not a paraphrase of it, over a timeline holding
// all three shapes it must tell apart.
func TestCleanupMigrationArchivesGeneratedRemindersOnly(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)

	generated := seedTaskRow(t, owner, e.WS, "Check in — no activity since 2026-06-16", "system", false)
	generatedCadence := seedTaskRow(t, owner, e.WS, "Time for a check-in — last touched 2026-06-16", "system", false)
	completed := seedTaskRow(t, owner, e.WS, "Check in — no activity since 2026-06-16", "system", true)
	handWritten := seedTaskRow(t, owner, e.WS, "Check in — no activity since the trade show", "manual", false)

	applyCleanupMigration(t, owner)

	for _, id := range []ids.UUID{generated, generatedCadence} {
		if !isArchived(t, owner, id) {
			t.Errorf("task %s minted by the engine was left in the work queue", id)
		}
	}
	if isArchived(t, owner, completed) {
		t.Error("a task somebody already completed was archived out from under finished work")
	}
	if isArchived(t, owner, handWritten) {
		t.Error("a human-authored task was archived — the migration must only reach the engine's own output")
	}
}

// runEligibilityScan drives one full time-scan pass at the pinned instant.
func runEligibilityScan(t *testing.T, e *Env) {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	scanner := compose.NewTimeScannerWithClock(e.Pool, func() time.Time { return eligibilityScanNow }, quiet)
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("time-scan pass: %v", err)
	}
}

// seedQuietTouch inserts one human-logged mail at quietSince and returns
// its id, leaving the caller to decide which timelines it lands on.
func seedQuietTouch(t *testing.T, owner *pgx.Conn, ws ids.UUID) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		 VALUES ($1, $2, 'email', 'Last genuine engagement', $3, 'manual', 'human:x')`,
		id, ws, quietSince); err != nil {
		t.Fatalf("seeding the quiet touch: %v", err)
	}
	return id
}

// linkQuietTouch is the one-record shorthand: a fresh quiet mail on
// exactly one entity's timeline.
func linkQuietTouch(t *testing.T, owner *pgx.Conn, ws ids.UUID, entityType string, entity ids.UUID) {
	t.Helper()
	linkTouch(t, owner, ws, seedQuietTouch(t, owner, ws), entityType, entity)
}

// linkTouch attaches an activity to any of the record types the candidate
// query knows — the harness's own LinkActivity only spans person and deal.
func linkTouch(t *testing.T, owner *pgx.Conn, ws, activity ids.UUID, entityType string, entity ids.UUID) {
	t.Helper()
	column, ok := map[string]string{
		"person": "person_id", "organization": "organization_id",
		"deal": "deal_id", "lead": "lead_id",
	}[entityType]
	if !ok {
		t.Fatalf("no activity_link column for entity type %q", entityType)
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity_link (workspace_id, activity_id, entity_type, `+column+`) VALUES ($1, $2, $3, $4)`,
		ws, activity, entityType, entity); err != nil {
		t.Fatalf("linking the touch to %s %s: %v", entityType, entity, err)
	}
}

// taskCountOn counts the reminder tasks standing on one record's timeline.
func taskCountOn(t *testing.T, e *Env, entityType string, entity ids.UUID) int {
	t.Helper()
	return e.WsCount(t, `
		SELECT count(*) FROM activity a
		JOIN activity_link al ON al.activity_id = a.id
		WHERE al.entity_type = $1
		  AND coalesce(al.person_id, al.organization_id, al.deal_id, al.lead_id) = $2
		  AND a.kind = 'task' AND a.archived_at IS NULL`, entityType, entity)
}

// backdateCreatedAt shifts a record's creation stamp through the owner
// connection: rows seeded by the module stores are created "now", and the
// creation grace reads created_at directly.
func backdateCreatedAt(t *testing.T, owner *pgx.Conn, table string, id ids.UUID, at time.Time) {
	t.Helper()
	if _, ok := map[string]struct{}{
		"person": {}, "organization": {}, "deal": {}, "lead": {},
	}[table]; !ok {
		t.Fatalf("backdating %q is not part of this fixture's vocabulary", table)
	}
	if _, err := owner.Exec(context.Background(),
		`UPDATE `+table+` SET created_at = $1 WHERE id = $2`, at, id); err != nil {
		t.Fatalf("backdating %s %s: %v", table, id, err)
	}
}

// attachDealToOrg puts the deal on the account, which is what makes the
// account itself worth reminding about.
func attachDealToOrg(t *testing.T, owner *pgx.Conn, deal, org ids.UUID) {
	t.Helper()
	if _, err := owner.Exec(context.Background(),
		`UPDATE deal SET organization_id = $1 WHERE id = $2`, org, deal); err != nil {
		t.Fatalf("attaching deal %s to organization %s: %v", deal, org, err)
	}
}

// seedStakeholderSeat gives a person a live seat on a deal.
func seedStakeholderSeat(t *testing.T, owner *pgx.Conn, ws, person, deal ids.UUID) {
	t.Helper()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO relationship (workspace_id, kind, person_id, deal_id, role, source, captured_by)
		 VALUES ($1, 'deal_stakeholder', $2, $3, 'champion', 'manual', 'human:x')`,
		ws, person, deal); err != nil {
		t.Fatalf("seeding the stakeholder seat: %v", err)
	}
}

// seedEmployment employs a person at an organization — a relationship the
// candidate query deliberately does NOT treat as live work.
func seedEmployment(t *testing.T, owner *pgx.Conn, ws, person, org ids.UUID) {
	t.Helper()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
		 VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`,
		ws, person, org); err != nil {
		t.Fatalf("seeding the employment edge: %v", err)
	}
}

// seedLeadInStatus inserts one lead in the given lifecycle status.
func seedLeadInStatus(t *testing.T, owner *pgx.Conn, ws ids.UUID, status string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO lead (id, workspace_id, full_name, status, source, captured_by)
		 VALUES ($1, $2, 'Inbound Lead', $3, 'manual', 'human:x')`,
		id, ws, status); err != nil {
		t.Fatalf("seeding a %s lead: %v", status, err)
	}
	return id
}

// seedTaskRow inserts one task activity with the exact subject, source and
// done state the cleanup migration has to tell apart.
func seedTaskRow(t *testing.T, owner *pgx.Conn, ws ids.UUID, subject, source string, done bool) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	var doneAt any
	if done {
		doneAt = quietSince
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, is_done, done_at, source, captured_by)
		 VALUES ($1, $2, 'task', $3, $4, $5, $6, $7, 'human:x')`,
		id, ws, subject, quietSince, done, doneAt, source); err != nil {
		t.Fatalf("seeding the %s task %q: %v", source, subject, err)
	}
	return id
}

// applyCleanupMigration runs the shipped 0148 up-migration statement
// again, against the fixture rows seeded above — the migration's own SQL
// is what this suite is asserting on, so it is loaded from the embedded
// namespace rather than restated here.
func applyCleanupMigration(t *testing.T, owner *pgx.Conn) {
	t.Helper()
	applyCoreMigration(t, owner, "0148")
}

// applyRestoreMigration runs the shipped 0149 up-migration, which gives back
// the reminders 0148 archived that nothing in the workspace will re-mint.
func applyRestoreMigration(t *testing.T, owner *pgx.Conn) {
	t.Helper()
	applyCoreMigration(t, owner, "0149")
}

// applyCoreMigration runs one shipped core migration's own SQL against the
// fixture rows, loaded from the embedded namespace rather than restated here —
// the migration is what these suites assert on.
func applyCoreMigration(t *testing.T, owner *pgx.Conn, version string) {
	t.Helper()
	core, err := migrations.Core()
	if err != nil {
		t.Fatalf("loading the core migration namespace: %v", err)
	}
	for _, m := range core.Migrations {
		if m.Version != version {
			continue
		}
		if _, err := owner.Exec(context.Background(), m.UpSQL); err != nil {
			t.Fatalf("applying migration %s: %v", version, err)
		}
		return
	}
	t.Fatalf("core migration %s is not in the embedded namespace", version)
}

// isArchived reports whether one activity row has been archived.
func isArchived(t *testing.T, owner *pgx.Conn, id ids.UUID) bool {
	t.Helper()
	var archived bool
	if err := owner.QueryRow(context.Background(),
		`SELECT archived_at IS NOT NULL FROM activity WHERE id = $1`, id).Scan(&archived); err != nil {
		t.Fatalf("reading the archival state of activity %s: %v", id, err)
	}
	return archived
}

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

// adopt makes a generated task somebody's own work, the way a person does:
// assigning it, or setting a reminder on it.
func adopt(t *testing.T, owner *pgx.Conn, id ids.UUID, column string, value any) {
	t.Helper()
	if _, err := owner.Exec(context.Background(),
		`UPDATE activity SET `+column+` = $2 WHERE id = $1`, id, value); err != nil {
		t.Fatalf("setting %s on task %s: %v", column, id, err)
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
	adopt(t, owner, assigned, "assignee_id", seedUser(t, owner, e.WS))
	adopt(t, owner, reminded, "remind_at", quietSince)

	applyCleanupMigration(t, owner)
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

func TestTheRestoreLeavesADeliberateHumanArchiveAlone(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)

	seedReminderAutomation(t, owner, e.WS, "no_activity_reminder", false)
	dismissed := seedTaskRow(t, owner, e.WS,
		"Check in — no activity since 2026-06-16", "system", false)
	adopt(t, owner, dismissed, "assignee_id", seedUser(t, owner, e.WS))
	// A person archived this one through the store, which writes the audit
	// row 0148's raw SQL never does. That entry is what tells the two apart.
	if _, err := owner.Exec(context.Background(),
		`UPDATE activity SET archived_at = now() WHERE id = $1`, dismissed); err != nil {
		t.Fatalf("archiving the task: %v", err)
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO audit_log (workspace_id, actor_type, actor_id, action, entity_type, entity_id)
		 VALUES ($1, 'human', 'human:x', 'archive', 'activity', $2)`,
		e.WS, dismissed); err != nil {
		t.Fatalf("recording the human archive: %v", err)
	}

	applyRestoreMigration(t, owner)

	if !isArchived(t, owner, dismissed) {
		t.Error("a reminder a person deliberately archived was handed back to them")
	}
}
