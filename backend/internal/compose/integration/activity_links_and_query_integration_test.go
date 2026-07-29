// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Two halves of the activity read the contract declared and the code did
// not deliver.
//
// links[] is on the Activity schema and is what a timeline's "which record
// is this about" chip is built from — but scanActivity never read the link
// rows, so every client that asked got null. q was declared as a query
// parameter and never reached the SQL, so a caller could not tell a
// silently-ignored filter from an honestly empty result.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
