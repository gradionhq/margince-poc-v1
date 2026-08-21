// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// last_activity_at on person and organization (PO-DDL-1/-4 as amended
// 2026-08-18): kept on the writes themselves by migration 1787032690's
// triggers, over a real migrated Postgres. A note on a contact moves the
// contact's clock and the clock of every account currently employing them; a
// note on a deal moves the deal's account; a back-dated capture never moves a
// clock backwards; an employment that starts later brings the contact's
// history to the account; archiving the newest activity moves a clock back;
// a clock move is not an edit (no version bump); and the two lists sort by it.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestLastActivity_MovesThePersonAndEveryAccountItReaches(t *testing.T) {
	e := Setup(t)
	// Seeded FIRST: on the default recency tie-break quiet would sort ahead of
	// the others, so its place at the end below can only be NULLS LAST.
	quiet := e.SeedOrg(t, "Quiet Clock", nil)
	acme := e.SeedOrg(t, "Acme Clock", nil)
	other := e.SeedOrg(t, "Other Clock", nil)
	late := e.SeedOrg(t, "Late Employer Clock", nil)
	staff := e.SeedPerson(t, "Works At Acme", nil)
	personID := ids.From[ids.PersonKind](staff)
	orgID := ids.From[ids.OrganizationKind](acme)
	if _, err := e.People.CreateRelationship(e.Admin(), people.CreateRelationshipInput{
		Kind: "employment", PersonID: &personID, OrganizationID: &orgID, IsCurrentPrimary: boolPtr(true), Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	pipeline, open := pipelineFixtureFor(e.Admin(), t, e.Deals)
	deal, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "Other's deal", AmountMinor: int64Ptr(100), Currency: strPtr("EUR"),
		PipelineID: pipeline, StageID: open, OrganizationID: orgIDPtr(orgIDOf(other)), Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}

	log := func(when time.Time, links ...activities.ActivityLinkInput) ids.UUID {
		t.Helper()
		subject := "touch"
		logged, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
			Kind: "note", Subject: &subject, OccurredAt: &when, Source: "manual", Links: links,
		})
		if err != nil {
			t.Fatalf("logging: %v", err)
		}
		return ids.UUID(logged.Id)
	}
	before, err := e.People.GetPerson(e.Admin(), personID, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if before.Version == nil {
		t.Fatal("a created person carries a version")
	}
	versionBefore := *before.Version

	newest := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	older := newest.Add(-72 * time.Hour)
	// The newest note carries TWO links (person and deal account), so archiving
	// it must move both clocks — the trigger recomputes per link row.
	newestNote := log(newest,
		activities.ActivityLinkInput{EntityType: "person", EntityID: staff},
		activities.ActivityLinkInput{EntityType: "organization", EntityID: other})
	// A back-dated capture arriving later must not move a clock backwards.
	log(older, activities.ActivityLinkInput{EntityType: "person", EntityID: staff})
	log(older, activities.ActivityLinkInput{EntityType: "deal", EntityID: ids.UUID(deal.Id)})

	person, err := e.People.GetPerson(e.Admin(), personID, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if person.LastActivityAt == nil || !person.LastActivityAt.Equal(newest) {
		t.Fatalf("person.last_activity_at = %v, want %v (the newest, not the last written)", person.LastActivityAt, newest)
	}
	clock := func(org ids.UUID) *time.Time {
		t.Helper()
		o, err := e.People.GetOrganization(e.Admin(), orgIDOf(org), storekit.LiveOnly)
		if err != nil {
			t.Fatal(err)
		}
		return o.LastActivityAt
	}
	if got := clock(acme); got == nil || !got.Equal(newest) {
		t.Fatalf("employer's last_activity_at = %v, want %v via the live employment", got, newest)
	}
	if got := clock(other); got == nil || !got.Equal(newest) {
		t.Fatalf("other's last_activity_at = %v, want %v via the direct link", got, newest)
	}
	if got := clock(quiet); got != nil {
		t.Fatalf("an account nothing reached has last_activity_at = %v, want NULL", got)
	}

	// A clock move is the timeline's, not an edit of the record: the person's
	// version is still what creation stamped, so an editor's If-Match holds.
	if person.Version == nil || *person.Version != versionBefore {
		t.Fatalf("person.version = %v after two notes, want %d unchanged — a clock move must not bump the version", person.Version, versionBefore)
	}

	// An employment that starts AFTER the notes brings the contact's history to
	// the new account: the reach set moved without any activity being written.
	lateID := ids.From[ids.OrganizationKind](late)
	if _, err := e.People.CreateRelationship(e.Admin(), people.CreateRelationshipInput{
		Kind: "employment", PersonID: &personID, OrganizationID: &lateID, IsCurrentPrimary: boolPtr(false), Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	if got := clock(late); got == nil || !got.Equal(newest) {
		t.Fatalf("new employer's last_activity_at = %v, want %v — the reach set moved", got, newest)
	}

	// Archiving the newest note moves the clocks BACK to the next-newest: the
	// column is a recompute from the live timeline, never a monotone high-water
	// mark that outlives what it counted.
	if _, err := e.Activities.ArchiveActivity(e.Admin(), ids.From[ids.ActivityKind](newestNote), nil); err != nil {
		t.Fatal(err)
	}
	person, err = e.People.GetPerson(e.Admin(), personID, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if person.LastActivityAt == nil || !person.LastActivityAt.Equal(older) {
		t.Fatalf("person.last_activity_at after archiving the newest = %v, want %v", person.LastActivityAt, older)
	}
	if got := clock(acme); got == nil || !got.Equal(older) {
		t.Fatalf("employer's last_activity_at after the archive = %v, want %v", got, older)
	}
	// The second link on the archived note: other falls back to its deal note.
	if got := clock(other); got == nil || !got.Equal(older) {
		t.Fatalf("other's last_activity_at after the archive = %v, want %v via the deal", got, older)
	}
	// Re-log the newest so the sort below has three distinct clocks again.
	log(newest, activities.ActivityLinkInput{EntityType: "person", EntityID: staff})

	// The list sorts by it, newest first: acme, other, then the untouched one.
	sort := "-last_activity_at"
	page, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{Sort: &sort})
	if err != nil {
		t.Fatalf("sorting organizations by last activity: %v", err)
	}
	var order []ids.UUID
	for _, o := range page {
		id := ids.UUID(o.Id)
		if id == acme || id == other || id == quiet {
			order = append(order, id)
		}
	}
	if len(order) != 3 || order[0] != acme || order[1] != other || order[2] != quiet {
		t.Fatalf("sort=-last_activity_at ordered %v, want acme, other, quiet (NULLS LAST)", order)
	}
}

// Two writers on one contact, the newer committing first and the older having
// waited on its row lock: the clock must end at the NEWER value. A recompute
// folded into a plain UPDATE stores the value it derived before the wait —
// the older one — because READ COMMITTED re-checks WHERE but not SET after a
// lock wait; move_last_activity locks the row first for exactly this reason.
// Driven with two owner connections and hand-written activity rows because
// the race is between transactions, and the real writer commits each of its
// own before returning.
func TestLastActivity_ConcurrentWritersConvergeOnTheNewest(t *testing.T) {
	e := Setup(t)
	staff := e.SeedPerson(t, "Raced Contact", nil)
	a := OwnerConn(t)
	b := OwnerConn(t)
	ctx := context.Background()
	newest := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	older := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	insert := func(tx pgx.Tx, when time.Time) error {
		id := ids.NewV7()
		if _, err := tx.Exec(ctx, `INSERT INTO activity
			(id, kind, subject, occurred_at, created_at, source, captured_by)
			VALUES ($1, 'note', 'race', $2, $2, 'manual', 'human:x')`, id, when); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO activity_link (activity_id, entity_type, person_id)
			VALUES ($1, 'person', $2)`, id, staff)
		return err
	}

	txA, err := a.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := insert(txA, newest); err != nil {
		t.Fatalf("A: %v", err)
	}
	// B starts after A holds the row lock and blocks inside its trigger.
	done := make(chan error, 1)
	go func() {
		txB, err := b.Begin(ctx)
		if err != nil {
			done <- err
			return
		}
		if err := insert(txB, older); err != nil {
			done <- err
			return
		}
		done <- txB.Commit(ctx)
	}()
	// Let B reach the lock, then release it by committing A. A fixed pause is
	// the only handle on "B is now waiting" from outside Postgres; if B has not
	// reached the lock yet the test still passes for the RIGHT reason (no race
	// occurred), it just proves less on that run.
	select {
	case err := <-done:
		t.Fatalf("B finished before A committed — it should have been blocked: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := txA.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("B: %v", err)
	}

	got, err := e.People.GetPerson(e.Admin(), ids.From[ids.PersonKind](staff), storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastActivityAt == nil || !got.LastActivityAt.Equal(newest) {
		t.Fatalf("last_activity_at = %v after the race, want %v — the waiting writer stored a stale derivation", got.LastActivityAt, newest)
	}
}

// The account clock reaches its answer through indexes, never through a scan
// of the whole timeline (migration 1787044474).
//
// The cost of getting this wrong lands on the WRITE path, which is why it is
// worth a gate: the clock is maintained by per-row triggers, so one scan per
// call means every employment written or ended, and every activity filed,
// costs one pass over activity_link. The shipped form compared the account id
// against three columns of a joined row — unsargable, so every call scanned —
// and seeding 5 000 employments against a 20 000-row timeline did not finish
// in 9m43s.
//
// Measured as work actually done rather than elapsed time: Postgres counts a
// sequential scan per table, so a run of calls that adds no scans proves the
// seeks happened, on any machine and under any load. A backend reports its
// statistics in batches, hence the explicit flush.
func TestLastActivity_TheAccountClockSeeksInsteadOfScanningTheTimeline(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	ctx := context.Background()

	acme := e.SeedOrg(t, "Seeking Clock", nil)
	staff := e.SeedPerson(t, "Employed At Seeking", nil)
	personID := ids.From[ids.PersonKind](staff)
	orgID := ids.From[ids.OrganizationKind](acme)
	if _, err := e.People.CreateRelationship(e.Admin(), people.CreateRelationshipInput{
		Kind: "employment", PersonID: &personID, OrganizationID: &orgID, IsCurrentPrimary: boolPtr(true), Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	newest := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	subject := "reaches the account through the employment"
	if _, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "note", Subject: &subject, OccurredAt: &newest, Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: staff}},
	}); err != nil {
		t.Fatal(err)
	}
	seedTimelineVolume(ctx, t, owner, 5000)

	const calls = 20
	before := sequentialScansOf(ctx, t, owner, "activity_link")
	for range calls {
		var clock *time.Time
		if err := owner.QueryRow(ctx, `SELECT last_activity_of_organization($1)`, acme).Scan(&clock); err != nil {
			t.Fatalf("reading the account clock: %v", err)
		}
		if clock == nil || !clock.Equal(newest) {
			t.Fatalf("last_activity_of_organization = %v, want %v — the employment arm must reach the contact's note", clock, newest)
		}
	}
	scans := sequentialScansOf(ctx, t, owner, "activity_link") - before
	if scans >= calls {
		t.Fatalf("%d clock reads cost %d sequential scans of activity_link, want fewer than one apiece — the account clock is scanning the timeline instead of seeking it", calls, scans)
	}
}

// seedTimelineVolume fills activity_link with background rows the account
// under test cannot reach, so that scanning the table is the more expensive
// plan and the planner's choice is not a coin toss.
//
// The rows hang off ONE lead. A lead link carries no person, deal or
// organization, so the maintenance trigger still fires on every row and finds
// nothing to move — real rows through the real write path, with none of the
// recompute cost that would make seeding the volume the slow part of the test.
func seedTimelineVolume(ctx context.Context, t *testing.T, owner *pgx.Conn, rows int) {
	t.Helper()
	var lead ids.UUID
	if err := owner.QueryRow(ctx, `INSERT INTO lead (full_name, source, captured_by)
		VALUES ('Timeline Volume', 'manual', 'human:x') RETURNING id`).Scan(&lead); err != nil {
		t.Fatalf("seeding the volume lead: %v", err)
	}
	if _, err := owner.Exec(ctx, `WITH act AS (
		INSERT INTO activity (kind, subject, occurred_at, source, captured_by)
		SELECT 'note', 'volume ' || i, now() - (i || ' minutes')::interval, 'manual', 'human:x'
		FROM generate_series(1, $2) AS i RETURNING id
	)
	INSERT INTO activity_link (activity_id, entity_type, lead_id)
	SELECT id, 'lead', $1 FROM act`, lead, rows); err != nil {
		t.Fatalf("seeding %d timeline rows: %v", rows, err)
	}
	// Without fresh statistics the planner sizes activity_link from whatever
	// the last analyze saw, which on a just-reset database is nothing.
	if _, err := owner.Exec(ctx, `ANALYZE activity, activity_link, relationship, deal`); err != nil {
		t.Fatalf("analyzing the seeded timeline: %v", err)
	}
}

// sequentialScansOf reads how many sequential scans Postgres has counted on a
// table, flushing this backend's pending statistics first so the number covers
// everything it has run.
func sequentialScansOf(ctx context.Context, t *testing.T, owner *pgx.Conn, table string) int64 {
	t.Helper()
	if _, err := owner.Exec(ctx, `SELECT pg_stat_force_next_flush()`); err != nil {
		t.Fatalf("flushing statistics: %v", err)
	}
	var scans int64
	if err := owner.QueryRow(ctx,
		`SELECT seq_scan FROM pg_stat_user_tables WHERE relname = $1`, table).Scan(&scans); err != nil {
		t.Fatalf("reading the sequential-scan count for %s: %v", table, err)
	}
	return scans
}
