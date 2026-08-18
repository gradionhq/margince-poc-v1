// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// What the TIMELINE read discloses when it is narrowed to a record the caller
// may not see.
//
// The claim answers successfully when it is broken, which is why it needs a
// database rather than an assertion over the built SQL: the activity scope is
// an ANY-LINK rule, so an activity reachable through one visible record passes
// it, and the narrowing column then filters on a record nobody checked. Delete
// the gate in ListActivitiesTx and this file is the only thing that notices.
//
// The sibling sweep (ListOpenTasks) has carried this gate since it shipped;
// the timeline did not, and widening listActivities' entity_type to admit
// `lead` and `project` is what made the gap reachable for those two arms.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A lead owned by another rep is not readable, so its timeline comes back
// EMPTY — the same answer an id that names nothing gets, and the same one a
// readable lead with no activities gets. That is what makes it existence-hiding:
// the caller cannot tell the three apart.
//
// It reads empty rather than not-found because listActivities documents 200 and
// 403 and no 404 (crm.yaml), and because on a list "nothing you may see matches"
// is the honest shape. An earlier version answered not-found on the reasoning
// that an empty page "tells the caller the lead is there" — it does not, for the
// reason above, and the 404 was an answer the contract never admitted.
func TestNarrowingTheTimelineToAnUnreadableLeadIsEmpty(t *testing.T) {
	e := setupPromises(t)
	hiddenLeadID, visiblePersonID, activityID := ids.NewV7(), ids.NewV7(), ids.NewV7()

	e.exec(t, `INSERT INTO lead (id, workspace_id, full_name, owner_id, source, captured_by)
		VALUES ($1, $2, 'Hidden Prospect', $3, 'seed', 'system')`,
		hiddenLeadID, e.ws, e.other)
	e.exec(t, `INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by)
		VALUES ($1, $2, 'Visible Contact', $3, 'seed', 'system')`,
		visiblePersonID, e.ws, e.rep)
	e.exec(t, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'note', 'Called about the rollout', now(), 'seed', 'system')`,
		activityID, e.ws)
	// Linked to BOTH: the caller may read this activity through the person, so
	// the any-link scope admits it. Only the narrowing target is out of reach,
	// which is precisely the case a scope check alone cannot catch.
	e.exec(t, `INSERT INTO activity_link (id, workspace_id, activity_id, entity_type, person_id)
		VALUES ($1, $2, $3, 'person', $4)`, ids.NewV7(), e.ws, activityID, visiblePersonID)
	e.exec(t, `INSERT INTO activity_link (id, workspace_id, activity_id, entity_type, lead_id)
		VALUES ($1, $2, $3, 'lead', $4)`, ids.NewV7(), e.ws, activityID, hiddenLeadID)

	leadType := "lead"
	store := NewStore(database.BindTo(e.pool, ids.From[ids.WorkspaceKind](e.ws)))
	got, _, err := store.ListActivities(e.as(), ListActivitiesInput{
		EntityType: &leadType, EntityID: &hiddenLeadID,
	})
	if err != nil {
		t.Fatalf("narrowing the timeline to another rep's lead → %v, want an empty page", err)
	}
	// The activity IS readable through the visible person, so the any-link
	// scope alone would hand it back. Empty proves the narrowing TARGET was
	// gated, which is the whole point of the check.
	if len(got) != 0 {
		t.Fatalf("narrowing to a lead owned by another rep returned %d activities — "+
			"the any-link scope admitted one through the person, and the narrowing "+
			"target was never gated", len(got))
	}
}

// The gate refuses what the caller may not see; it must not refuse what they
// may. Without this case the test above would pass against an implementation
// that denied every narrowed timeline read.
func TestNarrowingTheTimelineToAReadableLeadReturnsIt(t *testing.T) {
	e := setupPromises(t)
	ownLeadID, activityID := ids.NewV7(), ids.NewV7()

	e.exec(t, `INSERT INTO lead (id, workspace_id, full_name, owner_id, source, captured_by)
		VALUES ($1, $2, 'My Prospect', $3, 'seed', 'system')`, ownLeadID, e.ws, e.rep)
	e.exec(t, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'note', 'Asked for a Q4 quote', now(), 'seed', 'system')`,
		activityID, e.ws)
	e.exec(t, `INSERT INTO activity_link (id, workspace_id, activity_id, entity_type, lead_id)
		VALUES ($1, $2, $3, 'lead', $4)`, ids.NewV7(), e.ws, activityID, ownLeadID)

	leadType := "lead"
	store := NewStore(database.BindTo(e.pool, ids.From[ids.WorkspaceKind](e.ws)))
	got, _, err := store.ListActivities(e.as(), ListActivitiesInput{
		EntityType: &leadType, EntityID: &ownLeadID,
	})
	if err != nil {
		t.Fatalf("reading the timeline of the caller's OWN lead → %v, want the activity", err)
	}
	if len(got) != 1 || ids.UUID(got[0].Id) != activityID {
		t.Fatalf("own lead's timeline returned %d activities, want the one linked to it — "+
			"the gate is refusing reads it should admit", len(got))
	}
}

// ensureNarrowingTargetVisible is reached before any SQL is built, so an
// entity_type outside the link vocabulary is refused as a bad request rather
// than becoming a column name.
func TestNarrowingTheTimelineToAnUnknownEntityTypeIsRefused(t *testing.T) {
	e := setupPromises(t)
	someID := ids.NewV7()
	bogus := "invoice"
	store := NewStore(database.BindTo(e.pool, ids.From[ids.WorkspaceKind](e.ws)))
	_, _, err := store.ListActivities(e.as(), ListActivitiesInput{
		EntityType: &bogus, EntityID: &someID,
	})
	var invalid *InvalidLinkTypeError
	if !errors.As(err, &invalid) {
		t.Fatalf("narrowing to entity_type %q → %v, want InvalidLinkTypeError", bogus, err)
	}
}
