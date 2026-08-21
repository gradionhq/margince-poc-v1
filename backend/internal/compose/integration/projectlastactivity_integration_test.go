// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// project.last_activity_at (PROJ-FORM-6), kept by migration 1787320000's arm of
// the shared clock mover, over a real migrated Postgres. Filing an activity
// against a project moves its clock; relinking that activity onto another
// project moves both; archiving the newest moves the clock back to the
// next-newest; a clock move is not an edit, so the version and updated_at a
// caller holds still stand; and an activity that reaches the project only
// through one of its deals is deliberately NOT counted.

import (
	"context"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/migrations"
)

// logProjectNote files one note against the named links through the real
// activities writer and returns its id.
func logProjectNote(ctx context.Context, t *testing.T, e *Env, when time.Time, links ...activities.ActivityLinkInput) ids.UUID {
	t.Helper()
	subject := "touch"
	logged, _, err := e.Activities.LogActivity(ctx, activities.LogActivityInput{
		Kind: "note", Subject: &subject, OccurredAt: &when, Source: "manual", Links: links,
	})
	if err != nil {
		t.Fatalf("logging a note at %v: %v", when, err)
	}
	return ids.UUID(logged.Id)
}

// projectClock reads one project's stored clock through the real reader.
func projectClock(ctx context.Context, t *testing.T, e *Env, id ids.ProjectID) *time.Time {
	t.Helper()
	got, err := e.Deals.GetProject(ctx, id, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("reading project %v: %v", id, err)
	}
	return got.LastActivityAt
}

func TestProjectLastActivity_MovesOnEveryWriteThatChangesTheTimeline(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Clocked Client", nil)
	erp := seedProject(e.Admin(), t, e, "ERP replacement", strPtr("ERP-27"), org, nil)
	crm := seedProject(e.Admin(), t, e, "CRM rollout", strPtr("CRM-9"), org, nil)

	before, err := e.Deals.GetProject(e.Admin(), erp.ID, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if before.Version == nil || before.LastActivityAt != nil {
		t.Fatalf("a fresh project has version %v and last_activity_at %v, want a version and NULL",
			before.Version, before.LastActivityAt)
	}
	versionBefore, updatedBefore := *before.Version, before.UpdatedAt

	newest := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	older := newest.Add(-72 * time.Hour)
	newestNote := logProjectNote(e.Admin(), t, e, newest,
		activities.ActivityLinkInput{EntityType: "project", EntityID: erp.ID.UUID})
	// A back-dated note arriving second must not pull the clock backwards.
	logProjectNote(e.Admin(), t, e, older,
		activities.ActivityLinkInput{EntityType: "project", EntityID: erp.ID.UUID})

	if got := projectClock(e.Admin(), t, e, erp.ID); got == nil || !got.Equal(newest) {
		t.Fatalf("project.last_activity_at = %v, want %v (the newest, not the last written)", got, newest)
	}
	if got := projectClock(e.Admin(), t, e, crm.ID); got != nil {
		t.Fatalf("a project nothing was filed against has last_activity_at = %v, want NULL", got)
	}

	// A clock move is the timeline's, not an edit of the record: the version an
	// editor holds still matches, and updated_at has not moved either.
	after, err := e.Deals.GetProject(e.Admin(), erp.ID, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version == nil || *after.Version != versionBefore {
		t.Fatalf("project.version = %d after two notes, want %d unchanged — a clock move must not bump the version",
			derefVersion(after.Version), versionBefore)
	}
	if !after.UpdatedAt.Equal(updatedBefore) {
		t.Fatalf("project.updated_at = %v after two notes, want %v unchanged", after.UpdatedAt, updatedBefore)
	}

	// Relinking the newest note onto the other project moves BOTH clocks: the
	// one that lost it falls back to what is left, the one that gained it rises.
	if _, err := e.Activities.RelinkActivity(e.Admin(), ids.From[ids.ActivityKind](newestNote),
		activities.RelinkActivityInput{
			EntityType: "project", EntityID: crm.ID.UUID, ReplaceExistingOfType: true,
		}); err != nil {
		t.Fatalf("relinking the newest note onto the other project: %v", err)
	}
	if got := projectClock(e.Admin(), t, e, crm.ID); got == nil || !got.Equal(newest) {
		t.Fatalf("the receiving project's last_activity_at = %v, want %v", got, newest)
	}
	if got := projectClock(e.Admin(), t, e, erp.ID); got == nil || !got.Equal(older) {
		t.Fatalf("the losing project's last_activity_at = %v, want %v — the link moved away", got, older)
	}

	// Re-dating an activity moves the clock of the project it is filed against.
	// occurred_at and archived_at are the two columns the activity trigger
	// watches, and re-dating is the one of the pair that leaves the row live —
	// a clock that only tracked archiving would still pass every check below.
	redated := newest.Add(48 * time.Hour)
	if _, err := e.Activities.UpdateActivity(e.Admin(), ids.From[ids.ActivityKind](newestNote),
		activities.UpdateActivityInput{OccurredAt: &redated}); err != nil {
		t.Fatalf("re-dating the newest note: %v", err)
	}
	if got := projectClock(e.Admin(), t, e, crm.ID); got == nil || !got.Equal(redated) {
		t.Fatalf("the receiving project's last_activity_at after re-dating = %v, want %v", got, redated)
	}

	// Archiving the newest activity moves the clock back to the next-newest:
	// the column is a recompute from the live timeline, never a high-water mark.
	if _, err := e.Activities.ArchiveActivity(e.Admin(), ids.From[ids.ActivityKind](newestNote)); err != nil {
		t.Fatalf("archiving the newest note: %v", err)
	}
	if got := projectClock(e.Admin(), t, e, crm.ID); got != nil {
		t.Fatalf("the receiving project's last_activity_at after the archive = %v, want NULL — its only note is gone", got)
	}
}

// The clock counts activities filed against the PROJECT, not activities that
// reach it through one of its deals. The project timeline offers that union
// separately; the stored column stays one cheap, unambiguous question.
func TestProjectLastActivity_CountsOnlyDirectlyLinkedActivities(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Deal-Reached Client", nil)
	project := seedProject(e.Admin(), t, e, "Programme", strPtr("PRG-1"), org, nil)
	pipeline, open := pipelineFixtureFor(e.Admin(), t, e.Deals)
	deal, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "Phase one", AmountMinor: int64Ptr(100), Currency: strPtr("EUR"),
		PipelineID: pipeline, StageID: open, OrganizationID: orgIDPtr(orgIDOf(org)),
		ProjectID: &project.ID, Source: "manual",
	})
	if err != nil {
		t.Fatalf("creating a deal under the project: %v", err)
	}

	when := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	logProjectNote(e.Admin(), t, e, when,
		activities.ActivityLinkInput{EntityType: "deal", EntityID: ids.UUID(deal.Id)})

	if got := projectClock(e.Admin(), t, e, project.ID); got != nil {
		t.Fatalf("project.last_activity_at = %v after a note on its DEAL, want NULL — the clock counts direct links only", got)
	}
	// The derivation itself, not only the stored column: a note on the deal
	// never reaches the project's trigger, so widening last_activity_of_project
	// to walk the deals would leave the column NULL here and only surface on the
	// next backfill or rebuild.
	owner := OwnerConn(t)
	var derived *time.Time
	if err := owner.QueryRow(context.Background(),
		`SELECT last_activity_of_project($1)`, project.ID.UUID).Scan(&derived); err != nil {
		t.Fatalf("reading the project derivation: %v", err)
	}
	if derived != nil {
		t.Fatalf("last_activity_of_project = %v with only a deal note, want NULL — the derivation counts direct links only", derived)
	}
	if got, err := e.Deals.GetDeal(e.Admin(), ids.From[ids.DealKind](ids.UUID(deal.Id)), storekit.LiveOnly); err != nil {
		t.Fatal(err)
	} else if got.LastActivityAt == nil || !got.LastActivityAt.Equal(when) {
		t.Fatalf("the deal's own last_activity_at = %v, want %v", got.LastActivityAt, when)
	}
}

// projectClockMigrationVersion is the migration under test. Named once so a
// renumber is one edit and a wrong number is a loud "no such migration" rather
// than a silently skipped assertion.
const projectClockMigrationVersion = "1787320000"

// projectClockMigrationSQL is the shipped up-migration's own text, loaded from
// the embedded namespace. A paraphrase pasted here would drift from the file
// that actually runs, and drift is exactly what this test exists to catch.
func projectClockMigrationSQL(t *testing.T) string {
	t.Helper()
	core, err := migrations.Core()
	if err != nil {
		t.Fatalf("loading the core namespace: %v", err)
	}
	for _, m := range core.Migrations {
		if m.Version == projectClockMigrationVersion {
			return m.UpSQL
		}
	}
	t.Fatalf("core migration %s is not in the namespace — renumbered, or removed without removing this test",
		projectClockMigrationVersion)
	return ""
}

// The migration's backfill reaches rows that already carried links when it ran.
// Every installation this ships to has projects whose clock nothing ever wrote,
// and a project created after the migration can never tell a working backfill
// from a working trigger. The two are separated by turning the trigger off,
// writing the link the way the pre-migration product did, and re-running the
// migration's own SQL — which is idempotent, so replaying it is the same work
// an installation already did except for the rows the fixture just planted.
func TestProjectLastActivity_BackfillReachesRowsWrittenBeforeTheTrigger(t *testing.T) {
	e := Setup(t)
	ctx := context.Background()
	owner := OwnerConn(t)
	org := e.SeedOrg(t, "Backfilled Client", nil)
	project := seedProject(e.Admin(), t, e, "Legacy programme", strPtr("LEG-1"), org, nil)

	if _, err := owner.Exec(ctx, `ALTER TABLE activity_link DISABLE TRIGGER activity_link_project_last_activity`); err != nil {
		t.Fatalf("disabling the maintenance trigger: %v", err)
	}
	when := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	logProjectNote(e.Admin(), t, e, when,
		activities.ActivityLinkInput{EntityType: "project", EntityID: project.ID.UUID})
	if _, err := owner.Exec(ctx, `ALTER TABLE activity_link ENABLE TRIGGER activity_link_project_last_activity`); err != nil {
		t.Fatalf("re-enabling the maintenance trigger: %v", err)
	}
	if got := projectClock(e.Admin(), t, e, project.ID); got != nil {
		t.Fatalf("last_activity_at = %v with the trigger off, want NULL — the fixture did not reproduce a pre-migration row", got)
	}

	before, err := e.Deals.GetProject(e.Admin(), project.ID, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, projectClockMigrationSQL(t)); err != nil {
		t.Fatalf("replaying migration %s: %v", projectClockMigrationVersion, err)
	}
	after, err := e.Deals.GetProject(e.Admin(), project.ID, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastActivityAt == nil || !after.LastActivityAt.Equal(when) {
		t.Fatalf("last_activity_at after the backfill = %v, want %v", after.LastActivityAt, when)
	}
	// The backfill runs under the flag, so it does not invalidate the If-Match
	// version of every editor open when the migration lands.
	if before.Version == nil || after.Version == nil || *before.Version != *after.Version {
		t.Fatalf("project.version went %d → %d across the backfill, want unchanged",
			derefVersion(before.Version), derefVersion(after.Version))
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("project.updated_at went %v → %v across the backfill, want unchanged", before.UpdatedAt, after.UpdatedAt)
	}
}

// derefVersion reads a row version for a failure message; a nil one is reported
// as zero, which no live row ever carries.
func derefVersion(v *crmcontracts.RowVersion) int64 {
	if v == nil {
		return 0
	}
	return int64(*v)
}
