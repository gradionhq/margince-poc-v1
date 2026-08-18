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
	"testing"
	"time"

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
		Kind: "employment", PersonID: &personID, OrganizationID: &orgID, IsCurrentPrimary: true, Source: "manual",
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
		Kind: "employment", PersonID: &personID, OrganizationID: &lateID, IsCurrentPrimary: false, Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	if got := clock(late); got == nil || !got.Equal(newest) {
		t.Fatalf("new employer's last_activity_at = %v, want %v — the reach set moved", got, newest)
	}

	// Archiving the newest note moves the clocks BACK to the next-newest: the
	// column is a recompute from the live timeline, never a monotone high-water
	// mark that outlives what it counted.
	if _, err := e.Activities.ArchiveActivity(e.Admin(), ids.From[ids.ActivityKind](newestNote)); err != nil {
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
