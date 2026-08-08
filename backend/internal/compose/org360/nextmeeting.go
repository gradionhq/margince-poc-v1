// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The next meeting with this account, and who is in it.
//
// It is a FACT and it sits with the other facts, not with the suggestions: a
// meeting is on the calendar or it is not. The page's `meeting_ahead` moment
// says a meeting is coming and is worth preparing for; this says which one and
// with whom, so the "Prepare" action has something to prefill from.
//
// The section is ABSENT for two opposite reasons, and `sections_omitted` is the
// only thing that separates them: named there, the caller has no activity grant;
// not named, the grant is held and nothing is scheduled. "No meeting booked" is
// a fact a rep acts on; "you cannot see the calendar" is not the same statement,
// and a client that reads the missing field alone tells someone with no calendar
// access to book a meeting that already exists.
//
// The field cannot carry an explicit null to make this easier: the generated
// wire type is an omitempty pointer, so nil and absent are the same bytes. The
// omission list is the mechanism, which is also the convention every other
// section on this payload already follows.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// meetingParticipantLimit caps the attendee list. A meeting with more names
// than this is a webinar, and the page's job here is to say who is in the room
// well enough to prepare — not to be the attendee list, which the activity's
// own record already is.
const meetingParticipantLimit = 8

// nextMeetingSection reads the soonest meeting on the account that has not
// started yet, together with the attendees this caller may read.
//
// STARTS_AT, not created_at or due_at: a meeting's identity in time is when it
// happens. Ordering by anything else would put a long-booked meeting behind one
// entered this morning for next month.
//
// ONE query, not two. The 360 is a composite with a fixed query budget
// (TestOrganization360CostDoesNotGrowWithTheAccount), and a section that reads
// the row and then reads its children is how a composite starts costing more
// per account than per page. The attendees come back as JSON from a lateral
// sub-select, carrying their own row-scope predicate.
func nextMeetingSection(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time,
) (*crmcontracts.Organization360NextMeeting, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	nowPos := arg(now)
	activityScope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return nil, err
	}
	if activityScope == "" {
		activityScope = scopeAll
	}
	dealVisible, err := linkScope(ctx, "dl", arg)
	if err != nil {
		return nil, err
	}
	// Row-scoped per PERSON, not per meeting: the meeting is visible through any
	// of its links, so a rep who reaches it through their own contact must not
	// be handed the names of a colleague's contacts who were also in the room.
	//
	// DISTINCT because uq_activity_participant is unique on (activity, ROLE,
	// person): one person legitimately holds several roles on one meeting — a
	// captured email makes its sender `from` and `attendee`, a reply adds `to` —
	// and each is its own row. The question here is who is in the room, and the
	// answer for a person is once.
	personVisible, err := scopeClause(ctx, "person", "p", arg)
	if err != nil {
		return nil, err
	}

	var (
		meeting  crmcontracts.Organization360NextMeeting
		id       ids.UUID
		dealID   *ids.UUID
		occurred *time.Time
	)
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT a.id, a.occurred_at, coalesce(a.subject, ''),
		       (SELECT dl.deal_id FROM activity_link dl
		         WHERE dl.activity_id = a.id AND dl.entity_type = 'deal' AND %[3]s
		         ORDER BY dl.id LIMIT 1),
		       coalesce((
		         SELECT jsonb_agg(jsonb_build_object(
		                  'person_id', att.id, 'display_name', att.full_name)
		                ORDER BY att.full_name, att.id)
		           FROM (SELECT DISTINCT p.id, p.full_name
		                   FROM activity_participant ap
		                   JOIN person p ON p.id = ap.person_id
		                  WHERE ap.activity_id = a.id AND p.archived_at IS NULL AND %[5]s
		                  ORDER BY p.full_name, p.id
		                  LIMIT %[6]d) att
		       ), '[]'::jsonb)
		FROM activity a
		WHERE a.kind = 'meeting' AND a.archived_at IS NULL
		  -- A canceled meeting is still a future row. Offering it as the next
		  -- meeting sends a rep to prepare for something that will not happen,
		  -- and hides the real next one behind it. A NULL status is a manual
		  -- booking nobody has tracked a status on, which is a real meeting.
		  AND (a.meeting_status IS NULL OR a.meeting_status = 'booked')
		  AND a.occurred_at > $%[4]d
		  AND %[1]s AND %[2]s
		ORDER BY a.occurred_at, a.id
		LIMIT 1`,
		activityScope, activities.OrgLinkedActivityExists(orgPos), dealVisible, nowPos,
		personVisible, meetingParticipantLimit),
		args...,
	).Scan(&id, &occurred, &meeting.Subject, &dealID, &meeting.Participants)
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing scheduled. The caller holds the grant, so this is the account's
		// own state and the page says so.
		return nil, nil //nolint:nilnil // "no meeting booked" is the answer, not an absence of one
	}
	if err != nil {
		return nil, fmt.Errorf("org360: reading the next meeting: %w", err)
	}
	meeting.ActivityId = openapi_types.UUID(id)
	meeting.StartsAt = *occurred
	meeting.LinkedDealId = uuidPtr(dealID)
	return &meeting, nil
}
