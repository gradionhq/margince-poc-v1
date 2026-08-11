// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The two halves of the activity read a client can act on.
//
// links[] answers "which records is this about", and answers it within the
// caller's row scope: activity visibility is an any-link rule, so a link to
// a record the caller cannot read is dropped rather than disclosed. q
// filters subject and body, with its wildcards escaped as data.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestListActivitiesCarriesItsLinks(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	admin := e.Admin()

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	person := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	activity := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'email', 'Renewal terms', now(), 'manual', 'human:x')`, e.WS)
	// The organization arm is inserted here rather than through
	// LinkActivity, whose column map covers person and deal only.
	e.WsExec(t, `INSERT INTO activity_link (workspace_id, activity_id, entity_type, organization_id)
		VALUES ($1, $2, 'organization', $3)`, e.WS, activity, org)
	LinkActivity(t, owner, e.WS, activity, "person", person)

	got, _, err := e.Activities.ListActivities(admin, activities.ListActivitiesInput{})
	if err != nil {
		t.Fatalf("list activities: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("activities = %d, want 1", len(got))
	}
	if got[0].Links == nil {
		t.Fatal("links[] is null on a linked activity — the contract declares it and the timeline reads it")
	}
	linked := map[ids.UUID]string{}
	for _, link := range *got[0].Links {
		linked[ids.UUID(link.EntityId)] = string(link.EntityType)
	}
	if linked[org] != "organization" {
		t.Errorf("organization link = %q, want organization", linked[org])
	}
	if linked[person] != "person" {
		t.Errorf("person link = %q, want person", linked[person])
	}

	// The single-row read carries them too: one activity has one answer to
	// "what is this about", whichever endpoint asked.
	one, err := e.Activities.GetActivity(admin, ids.From[ids.ActivityKind](activity), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("get activity: %v", err)
	}
	if one.Links == nil || len(*one.Links) != 2 {
		t.Errorf("GetActivity links = %v, want the same two the list returned", one.Links)
	}
}

// Activity visibility is an ANY-link rule: one visible link makes the whole
// activity readable. The link PROJECTION cannot inherit that, or reading a
// shared contact's timeline would hand back the ids of the other records the
// same thread touches — records the caller's scope exists to hide.
func TestListActivitiesDropsLinksToRecordsOutOfRowScope(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	pipeline, stage, _ := DealFixture(t, e)

	mine := e.SeedPerson(t, "My Contact", &e.Rep1)
	theirDeal := e.SeedDeal(t, "Other team deal", pipeline, stage, &e.Rep3)
	activity := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'meeting', 'Joint call', now(), 'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, activity, "person", mine)
	LinkActivity(t, owner, e.WS, activity, "deal", theirDeal)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, activityLinkRepPerms)
	got, _, err := e.Activities.ListActivities(rep, activities.ListActivitiesInput{})
	if err != nil {
		t.Fatalf("list activities: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("activities = %d, want the one reachable through the visible contact", len(got))
	}
	if got[0].Links == nil {
		t.Fatal("links[] is null — the visible link must still be reported")
	}
	for _, link := range *got[0].Links {
		if ids.UUID(link.EntityId) == theirDeal {
			t.Errorf("links[] carries deal %v, which the caller cannot read — an any-link activity gate does not license disclosing every target",
				theirDeal)
		}
	}
	if len(*got[0].Links) != 1 || ids.UUID((*got[0].Links)[0].EntityId) != mine {
		t.Errorf("links[] = %+v, want only the visible contact %v", *got[0].Links, mine)
	}
}

// activityLinkRepPerms is a team-scoped rep: an unbounded caller short-circuits
// every scope clause, so the fixture must be bounded to test one at all.
var activityLinkRepPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"person":                {Read: true},
		"organization":          {Read: true},
		"deal":                  {Read: true},
		"activity":              {Read: true},
		"installation_settings": {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

func TestListActivitiesAppliesTheDeclaredQueryFilter(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	admin := e.Admin()

	wanted := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, body, occurred_at, source, captured_by)
		VALUES ($1, $2, 'note', 'Renewal terms', 'they asked about multi-year pricing', now(), 'manual', 'human:x')`, e.WS)
	SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, body, occurred_at, source, captured_by)
		VALUES ($1, $2, 'note', 'Onboarding kickoff', 'introductions only', now(), 'manual', 'human:x')`, e.WS)

	bySubject := "Renewal"
	got, _, err := e.Activities.ListActivities(admin, activities.ListActivitiesInput{Query: &bySubject})
	if err != nil {
		t.Fatalf("list with q: %v", err)
	}
	if len(got) != 1 || ids.UUID(got[0].Id) != wanted {
		t.Fatalf("q=%q matched %d activities, want only the one whose subject contains it", bySubject, len(got))
	}

	byBody := "multi-year"
	got, _, err = e.Activities.ListActivities(admin, activities.ListActivitiesInput{Query: &byBody})
	if err != nil {
		t.Fatalf("list with a body q: %v", err)
	}
	if len(got) != 1 || ids.UUID(got[0].Id) != wanted {
		t.Fatalf("q=%q matched %d activities, want the one whose body contains it", byBody, len(got))
	}

	// A wildcard in the query text is data, not syntax: a caller typing %
	// must not match everything.
	wildcard := "%"
	got, _, err = e.Activities.ListActivities(admin, activities.ListActivitiesInput{Query: &wildcard})
	if err != nil {
		t.Fatalf("list with a wildcard q: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("q=%q matched %d activities, want 0 — the wildcard must be escaped, not honored", wildcard, len(got))
	}
}
