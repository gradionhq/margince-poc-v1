// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

// "When we last met" — the prior meetings with this same room.
//
// It is the one fact a delivery review asks for first and the brief could not
// answer. Everything else on the page describes the state of play; this
// answers "what did we agree in this room last time", which is what a reader
// actually opens a recurring meeting wanting to know.
//
// It replaces the company-context line rather than adding a ninth section: the
// section list is a CLOSED enum by contract, and the brief is budgeted as a
// two-to-three-minute read. What it displaces was one sentence saying the
// company is where the lead attendee works — a fact the header already
// implies.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// priorMeetingCap bounds how far back the section looks. Two is the reader's
// question ("last time, and the time before"); a longer list is the timeline,
// which is a different surface with paging of its own.
const priorMeetingCap = 2

// priorMeeting is one earlier meeting with somebody from this room.
type priorMeeting struct {
	ID       ids.UUID
	Subject  string
	StartsAt time.Time
}

// readPriorMeetings finds the most recent meetings before this one that shared
// at least one attendee with it.
//
// Scoped three ways, and each is load-bearing:
//   - the activity row scope, so a meeting the caller may not discover is not
//     named to them;
//   - the same project rule the rest of the brief runs, so a scoped brief does
//     not reach into the other engagement for its history;
//   - overlapping attendees, because "this room" is the people, not the
//     recurring calendar entry — a series that changed its title is still the
//     same conversation, and two different meetings on one account are not.
//
// And it recalls only meetings that actually HAPPENED: earlier than this one,
// already past, and not cancelled or no-showed. "You met 4 days ago" about a
// meeting nobody attended is worse than saying nothing.
//
// project is the one the brief is narrowed by — the meeting's own filing,
// or the one the caller asked to prepare for — resolved by the caller so the
// history cannot read wider than the sections beside it.
func (s *Service) readPriorMeetings(ctx context.Context, tx pgx.Tx, room meeting, project *ids.ProjectID, now time.Time) ([]priorMeeting, error) {
	// room.Room, not room.Attendees: the latter is the DISPLAY list and stops
	// at eight, so matching on it would lose history shared only with the ninth
	// person in a large room.
	if len(room.Room) == 0 {
		return nil, nil
	}
	attendees := room.Room

	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	roomPos := arg(room.ID)
	// Earlier than this meeting AND already past. Constraining on the current
	// meeting alone would let a brief for a meeting three weeks out say "you
	// met" about one happening next Tuesday — a room that has not met yet.
	startsPos := arg(room.StartsAt)
	nowPos := arg(now)
	attendeePos := arg(attendees)
	// CONTENT, not discover. This section prints an earlier meeting's SUBJECT,
	// and ActivityDiscoverClause is documented as covering the safe markers
	// alone — a last-touch date, an open-task count. A reader who may know
	// that a conversation happened is not thereby entitled to read what it was
	// called, and picking the weaker clause for content is precisely the
	// defect restrictedreaders_test.go exists to catch.
	scope, err := auth.ActivityContentClause(ctx, "m", arg)
	if err != nil {
		return nil, err
	}
	if scope == "" {
		scope = scopeAll
	}
	// A project this brief is scoped to narrows the history to the same body of
	// work. Without one the rule is absent rather than permissive: an
	// unattributed meeting reads its whole shared history, which is what it had
	// before this section existed.
	within := scopeAll
	if project != nil {
		within = fmt.Sprintf(`(EXISTS (
			    SELECT 1 FROM activity_link ml
			    WHERE ml.activity_id = m.id AND ml.project_id = $%d)
			  OR NOT EXISTS (
			    SELECT 1 FROM activity_link mf
			    WHERE mf.activity_id = m.id AND mf.project_id IS NOT NULL))`,
			arg(project.UUID))
	}

	rows, err := tx.Query(ctx, fmt.Sprintf(priorMeetingsQuery, scope, within, roomPos, startsPos, nowPos, attendeePos, priorMeetingCap), args...)
	if err != nil {
		return nil, fmt.Errorf("read the earlier meetings: %w", err)
	}
	defer rows.Close()

	var out []priorMeeting
	for rows.Next() {
		var prior priorMeeting
		var subject *string
		if err := rows.Scan(&prior.ID, &subject, &prior.StartsAt); err != nil {
			return nil, fmt.Errorf("read an earlier meeting: %w", err)
		}
		prior.Subject = deref(subject)
		out = append(out, prior)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the earlier meetings: %w", err)
	}
	return out, nil
}

// priorMeetingsQuery reads the newest meetings before this one sharing a person
// with it. DISTINCT because a meeting with three of the same attendees is one
// meeting, not three.
const priorMeetingsQuery = `
	SELECT DISTINCT m.id, m.subject, m.occurred_at
	FROM activity m
	JOIN activity_participant mp ON mp.activity_id = m.id
	WHERE m.kind = 'meeting' AND m.archived_at IS NULL
	  AND m.id <> $%[3]d AND m.occurred_at < $%[4]d AND m.occurred_at <= $%[5]d
	  AND (m.meeting_status IS NULL OR m.meeting_status = 'held')
	  AND mp.person_id = ANY($%[6]d)
	  AND %[1]s
	  AND %[2]s
	ORDER BY m.occurred_at DESC
	LIMIT %[7]d`
