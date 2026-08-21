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
	"testing"

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
	person  ids.UUID
	erp     ids.ProjectID
	onERP   string
	onOther string
	unfiled string
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

	// Three exchanges with the same contact: one per engagement, and one
	// ordinary message nobody filed.
	mail := func(subject string, within *ids.ProjectID) string {
		logged, _, err := e.Activities.LogActivity(admin, activities.LogActivityInput{
			Kind: "email", Subject: &subject, Direction: strPtr("inbound"),
			Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: person}},
		})
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

	return scopeFixture{
		person: person, erp: erp,
		onERP:   mail("ERP cutover plan", &erp),
		onOther: mail("Rack decommissioning", &migration),
		unfiled: mail("Invoice question", nil),
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
	if len(got) != 3 {
		t.Fatalf("unscoped timeline = %d rows, want 3 — an absent scope narrows nothing", len(got))
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
		got, err := retriever.AssembleContext(e.Admin(), anchor, opts)
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
