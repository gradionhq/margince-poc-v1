// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Narrowing a read to ONE body of work, on both surfaces that carry it: the
// timeline list, and the context walk every assembled picture is built from.
//
// The rule is "filed under this project, or filed under none". The NEGATIVE
// half is what these prove — a test asserting only that the wanted rows appear
// would pass against a filter that does nothing at all, which is the failure
// mode a predicate like this actually has.

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// scopeFixture is one account running two engagements, plus ordinary
// correspondence belonging to neither — the shape the rule exists for.
type scopeFixture struct {
	person ids.UUID
	org    ids.UUID
	erp    ids.ProjectID
	// bystander is a second contact who appears ONLY in the other
	// engagement's mail — the hop-2 case: a scoped walk reaches people
	// through the activities it kept, so a person reachable only through a
	// dropped activity must not appear either.
	bystander ids.UUID
	onERP     string
	onOther   string
	unfiled   string
	// One open task and one future meeting per engagement, so the next-steps,
	// next-meeting and since-last-visit sections each have a row the scope
	// must drop and a row it must keep.
	erpTask      string
	otherTask    string
	erpMeeting   string
	otherMeeting string
	// otherAt is when the other engagement's mail arrived — the NEWEST
	// exchange on the account, so an unscoped last-touch date is this one and
	// a scoped read that still reports it has leaked.
	otherAt time.Time
}

// Everything here is written by the REAL writers — CreateProject, LogActivity
// and RelinkActivity — each through its own authority check and the audit +
// outbox write shape. Hand-inserted rows would let the filter pass over a row
// shape production never produces.
func seedTwoEngagementAccount(t *testing.T, e *Env) scopeFixture {
	t.Helper()
	admin := e.Admin()
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	person := e.SeedPerson(t, "Dana Buyer", &e.Rep1)

	newProject := func(name, key string) ids.ProjectID {
		p, err := e.Deals.CreateProject(admin, deals.CreateProjectInput{
			Name: name, Key: &key, OrganizationID: orgIDOf(org), Source: "manual",
		})
		if err != nil {
			t.Fatalf("create project %q: %v", name, err)
		}
		return projectIDOf(ids.UUID(p.Id))
	}
	erp := newProject("ERP rollout", "ERP-27")
	migration := newProject("Datacentre migration", "DC-4")
	bystander := e.SeedPerson(t, "Rack Vendor", &e.Rep1)

	// Three exchanges with the same contact on the same account: one per
	// engagement, and one ordinary message nobody filed. Each is linked to
	// the person AND the organization so both record pages read them.
	log := func(in activities.LogActivityInput, subject string, within *ids.ProjectID, occurredAt time.Time, others ...ids.UUID) string {
		in.Subject, in.OccurredAt = &subject, &occurredAt
		in.Links = []activities.ActivityLinkInput{
			{EntityType: "person", EntityID: person},
			{EntityType: "organization", EntityID: org},
		}
		for _, other := range others {
			in.Links = append(in.Links, activities.ActivityLinkInput{EntityType: "person", EntityID: other})
		}
		logged, _, err := e.Activities.LogActivity(admin, in)
		if err != nil {
			t.Fatalf("log %q: %v", subject, err)
		}
		if within != nil {
			id := ids.From[ids.ActivityKind](ids.UUID(logged.Id))
			if _, err := e.Activities.RelinkActivity(admin, id, activities.RelinkActivityInput{
				EntityType: "project", EntityID: within.UUID,
			}); err != nil {
				t.Fatalf("file %q under its project: %v", subject, err)
			}
		}
		return ids.UUID(logged.Id).String()
	}

	mail := func(subject string, within *ids.ProjectID, occurredAt time.Time, others ...ids.UUID) string {
		return log(activities.LogActivityInput{Kind: "email", Direction: strPtr("inbound")}, subject, within, occurredAt, others...)
	}
	task := func(subject string, within *ids.ProjectID) string {
		return log(activities.LogActivityInput{Kind: "task"}, subject, within, roomFixedNow.AddDate(0, 0, -1))
	}
	meeting := func(subject string, within *ids.ProjectID, startsAt time.Time) string {
		return log(activities.LogActivityInput{Kind: "meeting", MeetingStatus: strPtr("booked")}, subject, within, startsAt)
	}
	otherAt := roomFixedNow.AddDate(0, 0, -1)
	return scopeFixture{
		person: person, org: org, erp: erp, bystander: bystander, otherAt: otherAt,
		onERP:     mail("ERP cutover plan", &erp, roomFixedNow.AddDate(0, 0, -3)),
		onOther:   mail("Rack decommissioning", &migration, otherAt, bystander),
		unfiled:   mail("Invoice question", nil, roomFixedNow.AddDate(0, 0, -2)),
		erpTask:   task("Send ERP cutover checklist", &erp),
		otherTask: task("Book the rack haulier", &migration),
		// The other engagement's meeting is the SOONER one, so an unscoped
		// next-meeting read names it and a scoped read that still does has
		// leaked the other project.
		erpMeeting:   meeting("ERP go-live rehearsal", &erp, roomFixedNow.AddDate(0, 0, 5)),
		otherMeeting: meeting("Rack move walkthrough", &migration, roomFixedNow.AddDate(0, 0, 2)),
	}
}

func TestTimelineScopedToOneProjectDropsTheOtherEngagement(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)

	person := string(datasource.RecordPerson)
	got, _, err := e.Activities.ListActivities(e.Admin(), activities.ListActivitiesInput{
		EntityType: &person, EntityID: &f.person, WithinProjectID: &f.erp,
	})
	if err != nil {
		t.Fatalf("list within project: %v", err)
	}
	seen := map[string]bool{}
	for _, a := range got {
		seen[a.Id.String()] = true
	}

	if seen[f.onOther] {
		t.Error("the other engagement's mail survived a scoped read — the scope filtered nothing")
	}
	if !seen[f.onERP] {
		t.Error("the scoped project's own mail is missing")
	}
	// Attribution is optional, so unfiled mail is the account's general
	// history. Dropping it would leave a brief reading as though the
	// relationship had no past.
	if !seen[f.unfiled] {
		t.Error("mail filed under NO project was dropped; the rule keeps it")
	}
}

func TestTimelineWithoutAScopeStillSeesEveryEngagement(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)

	person := string(datasource.RecordPerson)
	got, _, err := e.Activities.ListActivities(e.Admin(), activities.ListActivitiesInput{
		EntityType: &person, EntityID: &f.person,
	})
	if err != nil {
		t.Fatalf("list unscoped: %v", err)
	}
	if len(got) != 7 {
		t.Fatalf("unscoped timeline = %d rows, want all 7 — an absent scope narrows nothing", len(got))
	}
}

// The context walk carries its OWN copy of the predicate, in another module a
// module may not import (ADR-0054). A test of the timeline list alone would
// pass with this half absent — and this is the half every assembled picture
// reads, so catch_me_up_on and prep_for_meeting hang off it.
func TestAssembledContextScopedToOneProjectDropsTheOtherEngagement(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	// AssembleContext embeds nothing — only Search does — so the walk needs no
	// embedder to answer.
	retriever := search.NewRetriever(search.NewStore(harnessDB(e.Pool, e.WS)), nil)
	anchor := datasource.EntityRef{Type: datasource.EntityPerson, ID: f.person}

	idsIn := func(opts retrieval.AssembleOptions) map[string]bool {
		t.Helper()
		return walkIDs(e.Admin(), t, retriever, anchor, opts)
	}

	scoped := idsIn(retrieval.AssembleOptions{MaxItems: 25, ProjectID: f.erp.String()})
	if scoped[f.onOther] {
		t.Error("the context walk carried the other engagement into a scoped picture")
	}
	if !scoped[f.onERP] {
		t.Error("the scoped project's own mail is missing from the walk")
	}
	if !scoped[f.unfiled] {
		t.Error("the walk dropped mail filed under no project; the rule keeps it")
	}

	// The same anchor unscoped still sees everything, so the narrowing above is
	// the scope's doing rather than something else in the walk quietly losing a
	// row — which would make the assertions pass for the wrong reason.
	wide := idsIn(retrieval.AssembleOptions{MaxItems: 25})
	if !wide[f.onOther] {
		t.Error("an unscoped walk lost the other engagement, so the scoped one proves nothing")
	}
}

// walkIDs flattens one context walk to the ids it carried, across every
// section.
func walkIDs(ctx context.Context, t *testing.T, retriever *search.Retriever, anchor datasource.EntityRef, opts retrieval.AssembleOptions) map[string]bool {
	t.Helper()
	got, err := retriever.AssembleContext(ctx, anchor, opts)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	out := map[string]bool{}
	for _, section := range got.Sections {
		for _, item := range section.Items {
			out[item.Ref.ID.String()] = true
		}
	}
	return out
}

// Hop 2 follows the activities hop 1 KEPT. A person reachable only through
// the dropped mail is outside the scope too; had hop 2 walked the unscoped
// timeline, the other engagement's contact would still be in the picture
// under a heading the scope claims to have narrowed.
//
// Anchored on the ACCOUNT: a person anchor never walks person neighbours (the
// anchor is not its own neighbour), so related_people only exists from here.
func TestAssembledContextScopedToOneProjectDropsPeopleReachedOnlyThroughTheOtherEngagement(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	retriever := search.NewRetriever(search.NewStore(harnessDB(e.Pool, e.WS)), nil)
	anchor := datasource.EntityRef{Type: datasource.EntityOrganization, ID: f.org}

	scoped := walkIDs(e.Admin(), t, retriever, anchor, retrieval.AssembleOptions{MaxItems: 25, ProjectID: f.erp.String()})
	if scoped[f.bystander.String()] {
		t.Error("a person linked only through the other engagement's mail reached the scoped walk's related_people")
	}
	if !scoped[f.person.String()] {
		t.Error("the contact on the scoped engagement's own mail is missing from related_people")
	}
	wide := walkIDs(e.Admin(), t, retriever, anchor, retrieval.AssembleOptions{MaxItems: 25})
	if !wide[f.bystander.String()] {
		t.Error("an unscoped walk lost the other engagement's contact, so the scoped absence proves nothing")
	}
}
