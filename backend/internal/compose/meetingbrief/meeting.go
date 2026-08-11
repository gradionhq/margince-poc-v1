// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

// The meeting the brief is about, read under the caller's own scope.
//
// One query, not four. The deal and the attendees come back beside the meeting
// row from sub-selects that each carry their own row-scope predicate, because a
// section that reads the row and then walks its children is how a composite
// read starts costing per attendee.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// attendeeCap bounds who is named. This is "who is in the room" for prep, not
// the invite list — past a handful the reader is scanning names instead of
// noticing who they have never met.
const attendeeCap = 8

// scopeAll is the predicate an unbounded caller gets. The clause helpers return
// empty for "narrows nothing", and an empty string is not a legal SQL fragment.
const scopeAll = "TRUE"

// meeting is the room, as the brief reads it.
type meeting struct {
	ID        ids.UUID
	Subject   string
	StartsAt  time.Time
	Deal      *dealRow
	Attendees []attendeeRow
}

// dealRow is the deal this meeting is linked to, if the caller may read it.
// Absent means either no linked deal or no deal grant, and the brief says
// neither: it simply has no deal line, because guessing which it was would be a
// claim about the caller's own permissions.
type dealRow struct {
	ID          ids.UUID
	Name        string
	Stage       string
	AmountMinor *int64
	Currency    string
	CloseDate   *time.Time
}

// attendeeRow is one person in the room, with what a reader needs to open a
// conversation with them: what they do, what seat they hold on the deal, and
// when we last spoke.
type attendeeRow struct {
	PersonID ids.UUID `json:"person_id"`
	FullName string   `json:"full_name"`
	Title    string   `json:"title"`
	DealRole string   `json:"deal_role"`
	// LastTouch is the newest conversation with this attendee BEFORE this
	// meeting. Null is the first-time flag: it means nothing was ever captured
	// with them, which is exactly "you have not met".
	LastTouch *time.Time `json:"last_touch"`
}

// firstTime reports whether the reader is meeting this person for the first
// time. It is derived rather than stored so it cannot disagree with the
// timestamp printed beside it.
func (a attendeeRow) firstTime() bool { return a.LastTouch == nil }

// readMeeting loads the meeting and its room.
//
// It refuses anything that is not a meeting. The route is reached from a
// meeting moment on the person page, and a "pre-meeting brief" over an email
// would be a brief about a conversation that has already happened — the reader
// would prepare for a room nobody booked.
func (s *Service) readMeeting(ctx context.Context, tx pgx.Tx, activityID ids.UUID) (meeting, error) {
	if err := auth.EnsureActivityVisibleLive(ctx, tx, activityID); err != nil {
		return meeting{}, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(activityID)
	dealScope, err := scopeFor(ctx, "deal", "dd", arg)
	if err != nil {
		return meeting{}, err
	}
	personScope, err := scopeFor(ctx, "person", "p", arg)
	if err != nil {
		return meeting{}, err
	}
	// The last-touch sub-select reads ACTIVITIES, so it takes the activity row
	// scope like every other activity read on this page. Without it the brief
	// reports when an attendee last spoke to us using a conversation this
	// caller may not open — the timing, and the fact that any correspondence
	// exists at all, are both disclosures the scope exists to prevent.
	touchScope, err := auth.ActivityScopeClause(ctx, "pa", arg)
	if err != nil {
		return meeting{}, err
	}
	if touchScope == "" {
		touchScope = scopeAll
	}

	var out meeting
	var subject *string
	var deal dealRow
	var dealID *ids.UUID
	var stage, currency *string
	var attendees []byte
	err = tx.QueryRow(ctx, fmt.Sprintf(meetingQuery, dealScope, personScope, idPos, touchScope), args...).
		Scan(&out.ID, &out.StartsAt, &subject,
			&dealID, &deal.Name, &stage, &deal.AmountMinor, &currency, &deal.CloseDate,
			&attendees)
	if errors.Is(err, pgx.ErrNoRows) {
		// The visibility probe passed, so the row exists and the caller may
		// read it; failing HERE means it is not a booked meeting. Not-found is
		// the honest answer for a brief that has no meeting to be about.
		return meeting{}, apperrors.ErrNotFound
	}
	if err != nil {
		return meeting{}, fmt.Errorf("read the meeting: %w", err)
	}

	if subject != nil {
		out.Subject = *subject
	}
	if dealID != nil {
		deal.ID = *dealID
		deal.Stage = deref(stage)
		deal.Currency = deref(currency)
		out.Deal = &deal
	}
	out.Attendees, err = decodeAttendees(attendees)
	if err != nil {
		return meeting{}, err
	}
	return out, nil
}

// meetingQuery reads the room in one statement. The two %s are the deal and
// person row-scope clauses, which decide what the caller is allowed to be told
// about rather than filtering it afterwards.
//
// last_touch is the most recent captured conversation with that attendee, and
// it is what makes the first-time flag honest: null means we have never
// exchanged anything with them, which is precisely "you have not met".
const meetingQuery = `
	SELECT a.id, a.occurred_at, a.subject,
	       d.id, COALESCE(d.name, ''), d.stage_name, d.amount_minor, d.currency, d.expected_close_date,
	       COALESCE((
	         SELECT json_agg(json_build_object(
	                  'person_id', p.id,
	                  'full_name', p.full_name,
	                  'title', COALESCE(p.title, ''),
	                  'deal_role', COALESCE(r.role, ''),
	                  'last_touch', (
	                    SELECT max(pa.occurred_at) FROM activity pa
	                    JOIN activity_participant pp ON pp.activity_id = pa.id
	                    WHERE pp.person_id = p.id AND pa.archived_at IS NULL
	                      AND pa.id <> a.id AND pa.occurred_at <= a.occurred_at
	                      AND %[4]s
	                  ))
	                ORDER BY p.full_name, p.id)
	         FROM (SELECT DISTINCT ap.person_id FROM activity_participant ap
	                WHERE ap.activity_id = a.id AND ap.person_id IS NOT NULL) parts
	         JOIN person p ON p.id = parts.person_id AND p.archived_at IS NULL
	         LEFT JOIN relationship r ON r.person_id = p.id AND r.deal_id = d.id
	              AND r.kind = 'deal_stakeholder' AND r.archived_at IS NULL
	         WHERE %[2]s
	       ), '[]'::json)
	FROM activity a
	LEFT JOIN LATERAL (
	  SELECT dd.id, dd.name, s.name AS stage_name, dd.amount_minor, dd.currency, dd.expected_close_date
	  FROM activity_link dl
	  JOIN deal dd ON dd.id = dl.deal_id AND dd.archived_at IS NULL
	  LEFT JOIN stage s ON s.id = dd.stage_id
	  WHERE dl.activity_id = a.id AND dl.deal_id IS NOT NULL AND %[1]s
	  ORDER BY dd.expected_close_date NULLS LAST, dd.id
	  LIMIT 1
	) d ON TRUE
	WHERE a.id = $%[3]d AND a.kind = 'meeting' AND a.archived_at IS NULL`

// scopeFor renders one object's row-scope clause, substituting the
// narrows-nothing predicate for the helper's empty string. An empty clause
// means the caller is unbounded for that object, never that the gate is skipped.
func scopeFor(ctx context.Context, object, alias string, arg func(any) int) (string, error) {
	clause, err := auth.ScopeClauseFor(ctx, object, alias, arg)
	if err != nil {
		return "", err
	}
	if clause == "" {
		return scopeAll, nil
	}
	return clause, nil
}

// decodeAttendees reads the sub-select's JSON into the room.
//
// The cap lands here, beside the type it bounds; the sub-select already carries
// the row-scope predicate that decides which names may appear at all.
func decodeAttendees(raw []byte) ([]attendeeRow, error) {
	var decoded []attendeeRow
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode the meeting attendees: %w", err)
	}
	if len(decoded) > attendeeCap {
		decoded = decoded[:attendeeCap]
	}
	return decoded, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
