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
	"errors"
	"testing"
	"time"

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
