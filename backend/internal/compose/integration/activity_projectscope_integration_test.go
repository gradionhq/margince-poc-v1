// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Narrowing a timeline read to ONE body of work.
//
// The rule is exclusion rather than selection, and the negative half is the
// one worth testing: a scope that only proved the wanted rows appear would
// pass against a filter that does nothing at all. So every case here asserts
// what is ABSENT as well as what is present.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// projectScopeFixture is one account running two engagements plus ordinary
// correspondence that belongs to neither — the shape the exclusion exists for.
type projectScopeFixture struct {
	org     ids.UUID
	person  ids.UUID
	erp     ids.ProjectID
	migrate ids.ProjectID
	onERP   ids.UUID
	onOther ids.UUID
	unfiled ids.UUID
}

func seedTwoProjectAccount(t *testing.T, e *Env) projectScopeFixture {
	t.Helper()
	owner := OwnerConn(t)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	person := e.SeedPerson(t, "Dana Buyer", &e.Rep1)

	newProject := func(name string) ids.UUID {
		return SeedIDRow(t, owner, `INSERT INTO project (id, owner_id, name, organization_id, source, captured_by)
			VALUES ($1, $2, $3, $4, 'manual', 'human:x')`, e.Rep1, name, org)
	}
	erp := newProject("ERP rollout")
	migrate := newProject("Datacentre migration")

	// Three messages with the same counterpart: one about each engagement, and
	// one ordinary exchange nobody filed. All three are linked to the person,
	// which is what a person-anchored read walks.
	mail := func(subject string) ids.UUID {
		id := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
			VALUES ($1, 'email', $2, now(), 'manual', 'human:x')`, subject)
		LinkActivity(t, owner, id, "person", person)
		return id
	}
	onERP := mail("ERP cutover plan")
	onOther := mail("Rack decommissioning")
	unfiled := mail("Invoice question")

	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, project_id)
		VALUES ($1, 'project', $2)`, onERP, erp)
	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, project_id)
		VALUES ($1, 'project', $2)`, onOther, migrate)

	return projectScopeFixture{
		org: org, person: person,
		erp: projectIDOf(erp), migrate: projectIDOf(migrate),
		onERP: onERP, onOther: onOther, unfiled: unfiled,
	}
}

func TestTimelineScopedToOneProjectDropsTheOtherEngagement(t *testing.T) {
	e := Setup(t)
	f := seedTwoProjectAccount(t, e)
	admin := e.Admin()

	person := string(datasource.RecordPerson)
	got, _, err := e.Activities.ListActivities(admin, activities.ListActivitiesInput{
		EntityType:      &person,
		EntityID:        &f.person,
		WithinProjectID: &f.erp,
	})
	if err != nil {
		t.Fatalf("list within project: %v", err)
	}

	seen := map[string]bool{}
	for _, a := range got {
		seen[a.Id.String()] = true
	}
	// The negative assertion is the one that fails when the predicate does
	// nothing, which is the failure mode a scope has.
	if seen[f.onOther.String()] {
		t.Error("the other engagement's mail survived a scoped read — the scope filtered nothing")
	}
	if !seen[f.onERP.String()] {
		t.Error("the scoped project's own mail is missing")
	}
	// Attribution is optional, so unfiled correspondence is the account's
	// general history and must survive. Dropping it would leave a brief
	// reading as though the relationship had no past.
	if !seen[f.unfiled.String()] {
		t.Error("correspondence filed under NO project was dropped; the scope excludes other projects, not everything unattributed")
	}
}

func TestTimelineWithoutAScopeStillSeesEveryEngagement(t *testing.T) {
	e := Setup(t)
	f := seedTwoProjectAccount(t, e)
	admin := e.Admin()

	person := string(datasource.RecordPerson)
	got, _, err := e.Activities.ListActivities(admin, activities.ListActivitiesInput{
		EntityType: &person,
		EntityID:   &f.person,
	})
	if err != nil {
		t.Fatalf("list unscoped: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("unscoped timeline = %d rows, want all 3 — an absent scope must narrow nothing", len(got))
	}
}
