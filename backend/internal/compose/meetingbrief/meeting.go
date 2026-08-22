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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
	Project   *projectRow
	Attendees []attendeeRow
	// Room is every attendee this caller may see, uncapped. Attendees above is
	// the DISPLAY list and stops at attendeeCap, so a reader is not scanning
	// names — but "who was in this room" is a different question from "who do
	// we name", and history shared only with the ninth person is still this
	// room's history.
	Room []ids.UUID
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

// projectRow is the body of work this meeting belongs to, if the meeting is
// filed under one and the caller may read it. Absent is the ordinary case:
// most meetings carry no project, and the brief simply says nothing about one.
type projectRow struct {
	ID            ids.UUID
	Name          string
	Key           string
	Phase         string
	TargetEndDate *time.Time
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
//
// requested is the project the caller asked to prepare for, already gated.
// It narrows the attendees' last-touch dates only when the meeting itself is
// filed under no project: a filed meeting's own project wins, and a request
// that disagrees with it is refused by resolveScope after this read.
func (s *Service) readMeeting(ctx context.Context, tx pgx.Tx, activityID ids.UUID, requested *ids.ProjectID) (meeting, error) {
	if err := auth.EnsureActivityContentVisibleLive(ctx, tx, activityID); err != nil {
		return meeting{}, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(activityID)
	requestedPos := arg(nullableProject(requested))
	clauses, err := roomClauses(ctx, arg)
	if err != nil {
		return meeting{}, err
	}

	var out meeting
	var subject *string
	var deal dealRow
	var dealID *ids.UUID
	var stage, currency *string
	var attendees []byte
	var project projectRow
	var projectID *ids.UUID
	err = tx.QueryRow(ctx, fmt.Sprintf(meetingQuery, clauses.deal, clauses.person, idPos, clauses.touch, clauses.seat, clauses.project, requestedPos), args...).
		Scan(&out.ID, &out.StartsAt, &subject,
			&dealID, &deal.Name, &stage, &deal.AmountMinor, &currency, &deal.CloseDate,
			&projectID, &project.Name, &project.Key, &project.Phase, &project.TargetEndDate,
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
	if projectID != nil {
		project.ID = *projectID
		out.Project = &project
	}
	if dealID != nil {
		deal.ID = *dealID
		deal.Stage = deref(stage)
		deal.Currency = deref(currency)
		out.Deal = &deal
	}
	full, err := decodeAttendees(attendees)
	if err != nil {
		return meeting{}, err
	}
	for _, attendee := range full {
		out.Room = append(out.Room, attendee.PersonID)
	}
	out.Attendees = full
	if len(out.Attendees) > attendeeCap {
		out.Attendees = out.Attendees[:attendeeCap]
	}
	return out, nil
}

// meetingQuery reads the room in one statement. The %s are the deal, person and
// last-touch row-scope clauses, which decide what the caller is allowed to be
// told about rather than filtering it afterwards, plus the seat edge's own
// admission on the LEFT JOIN that carries the deal role.
//
// last_touch is the most recent captured conversation with that attendee, and
// it is what makes the first-time flag honest: null means we have never
// exchanged anything with them, which is precisely "you have not met".
const meetingQuery = `
	SELECT a.id, a.occurred_at, a.subject,
	       d.id, COALESCE(d.name, ''), d.stage_name, d.amount_minor, d.currency, d.expected_close_date,
	       pr.id, COALESCE(pr.name, ''), COALESCE(pr.key, ''), COALESCE(pr.phase, ''), pr.target_end_date,
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
	                      -- Scoped with the rest of the brief. This is the number
	                      -- a reader trusts most, and it is the one that leaks:
	                      -- narrow the deal and leave this alone and the brief
	                      -- says "last spoke 3 days ago" counting a conversation
	                      -- about the other engagement entirely. The meeting's
	                      -- own filing wins; a requested project narrows only a
	                      -- meeting filed under none.
	                      AND (COALESCE(pr.id, $%[7]d::uuid) IS NULL OR EXISTS (
	                            SELECT 1 FROM activity_link tl
	                            WHERE tl.activity_id = pa.id AND tl.project_id = COALESCE(pr.id, $%[7]d::uuid))
	                          OR NOT EXISTS (
	                            SELECT 1 FROM activity_link tf
	                            WHERE tf.activity_id = pa.id AND tf.project_id IS NOT NULL))
	                  ))
	                ORDER BY p.full_name, p.id)
	         FROM (SELECT DISTINCT ap.person_id FROM activity_participant ap
	                WHERE ap.activity_id = a.id AND ap.person_id IS NOT NULL) parts
	         JOIN person p ON p.id = parts.person_id AND p.archived_at IS NULL
	         LEFT JOIN relationship r ON r.person_id = p.id AND r.deal_id = d.id
	              AND r.kind = 'deal_stakeholder' AND r.archived_at IS NULL
	              AND %[5]s
	         WHERE %[2]s
	       ), '[]'::json)
	FROM activity a
	LEFT JOIN LATERAL (
	  SELECT prj.id, prj.name, prj.key, prj.phase, prj.target_end_date
	  FROM activity_link pl
	  JOIN project prj ON prj.id = pl.project_id AND prj.archived_at IS NULL
	  WHERE pl.activity_id = a.id AND pl.project_id IS NOT NULL AND %[6]s
	  LIMIT 1
	) pr ON TRUE
	LEFT JOIN LATERAL (
	  SELECT dd.id, dd.name, s.name AS stage_name, dd.amount_minor, dd.currency, dd.expected_close_date
	  FROM activity_link dl
	  JOIN deal dd ON dd.id = dl.deal_id AND dd.archived_at IS NULL
	  LEFT JOIN stage s ON s.id = dd.stage_id
	  WHERE dl.activity_id = a.id AND dl.deal_id IS NOT NULL AND %[1]s
	  -- The meeting's own project decides WHICH deal this brief is about. An
	  -- account running two engagements carries two open deals, and picking by
	  -- close date alone named whichever happened to land first — a header
	  -- confidently about the other body of work. Nulls sort last, so an
	  -- unattributed meeting keeps the old ordering exactly.
	  ORDER BY (pr.id IS NOT NULL AND dd.project_id = pr.id) DESC,
	           dd.expected_close_date NULLS LAST, dd.id
	  LIMIT 1
	) d ON TRUE
	WHERE a.id = $%[3]d AND a.kind = 'meeting' AND a.archived_at IS NULL`

// roomScopes is every row-scope clause the meeting statement composes, each
// deciding what the caller may be TOLD rather than filtering afterwards.
type roomScopes struct {
	deal, person, project, touch, seat string
}

// roomClauses renders the five clauses in bind order.
//
// The last-touch sub-select reads ACTIVITIES, so it takes the activity row
// scope like every other activity read on this page. Without it the brief
// reports when an attendee last spoke to us using a conversation this
// caller may not open — the timing, and the fact that any correspondence
// exists at all, are both disclosures the scope exists to prevent.
//
// The seat's ROLE is the only thing the edge contributes, so a caller
// refused the edge gets the join matched away rather than the room emptied:
// `deal_role` reads as the empty string, exactly as it does for an attendee
// who holds no seat. Nothing is filtered, so the attendee list and its cap
// are untouched — which is what keeps this a withheld FIELD and not a
// withheld section, on a response that carries no channel to name one.
func roomClauses(ctx context.Context, arg func(any) int) (roomScopes, error) {
	var out roomScopes
	var err error
	if out.deal, err = scopeFor(ctx, "deal", "dd", arg); err != nil {
		return roomScopes{}, err
	}
	if out.person, err = scopeFor(ctx, "person", "p", arg); err != nil {
		return roomScopes{}, err
	}
	if out.project, err = projectJoinPredicate(ctx, "prj", arg); err != nil {
		return roomScopes{}, err
	}
	if out.touch, err = auth.ActivityDiscoverClause(ctx, "pa", arg); err != nil {
		return roomScopes{}, err
	}
	if out.touch == "" {
		out.touch = scopeAll
	}
	if out.seat, err = seatJoinPredicate(ctx, "r", arg); err != nil {
		return roomScopes{}, err
	}
	return out, nil
}

// nullableProject binds a requested project as a nullable uuid, so the
// meeting statement has one shape whether or not a project was requested.
func nullableProject(requested *ids.ProjectID) *ids.UUID {
	if requested == nil {
		return nil
	}
	return &requested.UUID
}

// seatJoinPredicate renders the edge's admission as a JOIN predicate: the
// endpoint conjunction for a caller who holds the edge grant, and the
// never-matches predicate for one who does not.
//
// A refusal becomes `false` rather than an error because this edge decorates a
// row it does not select. The alternative shapes are both worse: failing the
// read would deny a meeting brief the caller may otherwise see in full, and
// dropping the attendee would make a withheld ROLE look like an absent PERSON.
func seatJoinPredicate(ctx context.Context, alias string, arg func(any) int) (string, error) {
	clause, err := auth.EdgeReadScope(ctx, alias, arg)
	if errors.Is(err, apperrors.ErrPermissionDenied) {
		return "FALSE", nil
	}
	if err != nil {
		return "", err
	}
	if clause == "" {
		return scopeAll, nil
	}
	return clause, nil
}

// scopeFor renders one object's row-scope clause, substituting the
// narrows-nothing predicate for the helper's empty string. An empty clause
// means the caller is unbounded for that object, never that the gate is skipped.
// projectJoinPredicate admits the meeting's engagement, or matches it away.
//
// TWO GATES, not one, and conflating them is what this function exists to
// prevent. Row scope (scopeFor below) decides WHICH projects a caller may see;
// the OBJECT grant decides whether they may see projects at all. Since projects
// became workspace-readable, row scope returns an unrestricted clause to
// everyone — so a caller holding no project grant would have read the
// engagement's name, key, phase and target date straight off a meeting they
// could otherwise open.
//
// A refusal becomes a never-match rather than an error, the way the seat edge
// beside it does: the project decorates a brief it does not select, so a caller
// without the grant gets the brief with no project line — the same shape as a
// meeting filed under nothing — rather than losing a brief they may read.
func projectJoinPredicate(ctx context.Context, alias string, arg func(any) int) (string, error) {
	if err := auth.Require(ctx, "project", principal.ActionRead); err != nil {
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return "FALSE", nil
		}
		return "", err
	}
	return scopeFor(ctx, "project", alias, arg)
}

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

// decodeAttendees reads the sub-select's JSON into the room, UNCAPPED.
//
// The display cap is applied by the caller, which also keeps the full set: a
// list bounded here would make "who was in this room" unanswerable, and the
// prior-meeting history is matched on the room rather than on the names the
// brief prints. The sub-select already carries the row-scope predicate that
// decides which names may appear at all.
func decodeAttendees(raw []byte) ([]attendeeRow, error) {
	var decoded []attendeeRow
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode the meeting attendees: %w", err)
	}
	return decoded, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
