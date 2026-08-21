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
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Another rep's unpromoted capture is not readable — capture privacy holds it
// to its own owner, and every other shareable record type is read by every
// seat (platform/auth tableclass.go). Asking for its timeline is answered
// not-found, the same as an id that names nothing. Any other answer, including
// an empty page, tells the caller the company is there.
func TestNarrowingTheTimelineToAnUnreadableCompanyAnswersNotFound(t *testing.T) {
	e := setupPromises(t)
	hiddenOrgID, visiblePersonID, activityID := ids.NewV7(), ids.NewV7(), ids.NewV7()

	e.exec(t, `INSERT INTO organization (id, display_name, owner_id, visibility, source, captured_by)
		VALUES ($1, 'Zeta GmbH', $2, 'owner', 'seed', 'system')`, hiddenOrgID, e.other)
	e.exec(t, `INSERT INTO person (id, full_name, owner_id, source, captured_by)
		VALUES ($1, 'Visible Contact', $2, 'seed', 'system')`,
		visiblePersonID, e.rep)
	e.exec(t, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'note', 'Called about the rollout', now(), 'seed', 'system')`,
		activityID)
	// Linked to BOTH: the caller may read this activity through the person, so
	// the any-link scope admits it. Only the narrowing target is out of reach,
	// which is precisely the case a scope check alone cannot catch.
	e.exec(t, `INSERT INTO activity_link (id, activity_id, entity_type, person_id)
		VALUES ($1, $2, 'person', $3)`, ids.NewV7(), activityID, visiblePersonID)
	e.exec(t, `INSERT INTO activity_link (id, activity_id, entity_type, organization_id)
		VALUES ($1, $2, 'organization', $3)`, ids.NewV7(), activityID, hiddenOrgID)

	orgType := "organization"
	store := NewStore(database.BindTo(e.pool, ids.From[ids.WorkspaceKind](e.ws)))
	_, _, err := store.ListActivities(e.as(), ListActivitiesInput{
		EntityType: &orgType, EntityID: &hiddenOrgID,
	})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("narrowing the timeline to another rep's unpromoted capture → %v, want "+
			"ErrNotFound — an answer of any kind confirms the company exists and that "+
			"this activity happened on it", err)
	}
}

// The gate refuses what the caller may not see; it must not refuse what they
// may. Without this case the test above would pass against an implementation
// that denied every narrowed timeline read.
func TestNarrowingTheTimelineToAReadableLeadReturnsIt(t *testing.T) {
	e := setupPromises(t)
	ownLeadID, activityID := ids.NewV7(), ids.NewV7()

	e.exec(t, `INSERT INTO lead (id, full_name, owner_id, source, captured_by)
		VALUES ($1, 'My Prospect', $2, 'seed', 'system')`, ownLeadID, e.rep)
	e.exec(t, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'note', 'Asked for a Q4 quote', now(), 'seed', 'system')`,
		activityID)
	e.exec(t, `INSERT INTO activity_link (id, activity_id, entity_type, lead_id)
		VALUES ($1, $2, 'lead', $3)`, ids.NewV7(), activityID, ownLeadID)

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
