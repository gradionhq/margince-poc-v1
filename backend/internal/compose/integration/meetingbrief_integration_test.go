// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The pre-meeting brief's admission, against a real database.
//
// The brief reads a meeting, the people in the room, and what they promised.
// That is three record types, and a caller who may not read them must not
// reach any of it through this door — row scope decides WHICH meetings somebody
// sees, never whether they may see meetings at all.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/meetingbrief"
	"github.com/gradionhq/margince/backend/internal/compose/person360"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func meetingBriefService(e *Env) *meetingbrief.Service {
	view := person360.NewService(e.Pool, e.People, consent.NewStore(e.Pool),
		ai.NewFeedbackStore(e.Pool), func() time.Time { return roomFixedNow })
	return meetingbrief.NewService(e.Pool, view, e.People, func() time.Time { return roomFixedNow })
}

// The object grant is checked before any row is read. Without it a reader whose
// role grants no activity read still reaches a brief describing a meeting, its
// attendees and their commitments — through a door every sibling read refuses.
func TestMeetingBriefRefusesACallerWithNoActivityGrant(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	meeting := SeedRow(t, owner, `INSERT INTO activity
		(id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'meeting', 'Expansion review', now() + interval '1 day',
		        'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, meeting, "person", mine)

	perms := roomPerms
	perms.Objects = map[string]principal.ObjectGrant{
		// Everything the brief touches EXCEPT the activity it is about.
		"person":       {Read: true},
		"organization": {Read: true},
		"relationship": {Read: true},
		"deal":         {Read: true},
	}
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, perms)

	_, err := meetingBriefService(e).Get(rep, meeting)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("brief without an activity grant → %v, want ErrPermissionDenied", err)
	}
}

// The brief names the people in the room, so it is a person read too.
func TestMeetingBriefRefusesACallerWithNoPersonGrant(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	meeting := SeedRow(t, owner, `INSERT INTO activity
		(id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'meeting', 'Expansion review', now() + interval '1 day',
		        'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, meeting, "person", mine)

	perms := roomPerms
	perms.Objects = map[string]principal.ObjectGrant{"activity": {Read: true}}
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, perms)

	_, err := meetingBriefService(e).Get(rep, meeting)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("brief without a person grant → %v, want ErrPermissionDenied", err)
	}
}

// A meeting the caller cannot reach is a NOT FOUND, never an empty brief: an
// empty brief confirms the meeting exists and only its contents are withheld,
// which is the disclosure existence-hiding exists to prevent.
//
// An activity carries no owner of its own — the CHECK forbids an assignee on
// anything but a task — so its visibility is inherited from the records it is
// linked to. A meeting linked only to another team's contact is therefore
// outside this caller's scope.
func TestMeetingBriefRefusesAMeetingItCannotReach(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	meeting := SeedRow(t, owner, `INSERT INTO activity
		(id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'meeting', 'Their review', now() + interval '1 day',
		        'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, meeting, "person", theirs)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)

	_, err := meetingBriefService(e).Get(rep, meeting)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("brief on a meeting linked only to another team's contact → %v, want ErrNotFound", err)
	}
}

// An attendee's "last touch" is read from ACTIVITIES, so it takes the activity
// row scope like every other activity read on this page.
//
// The two tables diverge legitimately: participants are resolved from message
// headers, links are supplied by the connector. So a conversation can name this
// caller's attendee as a participant while being linked only to another team's
// contact — and an unscoped sub-select would report when that conversation
// happened, disclosing both its timing and that it exists at all.
func TestMeetingBriefDoesNotReportALastTouchTheCallerCannotRead(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	attendee := e.SeedPerson(t, "Ana Roth", &e.Rep1)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)

	meeting := SeedRow(t, owner, `INSERT INTO activity
		(id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'meeting', 'Expansion review', now() + interval '1 day',
		        'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, meeting, "person", attendee)
	seatInRoom(t, owner, e.WS, meeting, attendee)

	// A conversation this caller may not read, which nonetheless names their
	// attendee as a participant.
	hidden := SeedRow(t, owner, `INSERT INTO activity
		(id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'email', 'Cc: budget', now() - interval '3 days',
		        'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, hidden, "person", theirs)
	seatInRoom(t, owner, e.WS, hidden, attendee)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	brief, err := meetingBriefService(e).Get(rep, meeting)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// The attendee is a FIRST-TIME attendee as far as this caller can see:
	// the only prior conversation naming them is one they may not read. The
	// brief must say so — reporting a last touch would disclose both when that
	// conversation happened and that it happened at all.
	var attendees string
	for _, section := range brief.Sections {
		if section.Kind != "attendees" {
			continue
		}
		for _, sentence := range section.Sentences {
			attendees += sentence.Text + "\n"
		}
	}
	if attendees == "" {
		t.Fatal("the brief rendered no attendees section, so this proves nothing")
	}
	if !strings.Contains(attendees, "Ana Roth") {
		t.Fatalf("the attendee is missing from the room: %q", attendees)
	}
	if !strings.Contains(attendees, "first") {
		t.Errorf("attendees = %q; want Ana Roth flagged as first-time — the only prior conversation is one this caller cannot read", attendees)
	}
}

// seatInRoom names a person as a participant on an activity — the table the
// brief reads its room from, written separately from activity_link.
func seatInRoom(t *testing.T, owner *pgx.Conn, ws, activity, person ids.UUID) {
	t.Helper()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity_participant (workspace_id, activity_id, role, person_id)
		 VALUES ($1, $2, 'attendee', $3)`, ws, activity, person); err != nil {
		t.Fatalf("seating a participant: %v", err)
	}
}
