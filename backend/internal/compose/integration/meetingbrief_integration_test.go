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
	view := person360.NewService(e.Pool, e.People, consent.NewStore(e.DB()),
		ai.NewFeedbackStore(e.DB()), func() time.Time { return roomFixedNow })
	return meetingbrief.NewService(e.Pool, view, e.People, func() time.Time { return roomFixedNow })
}

// The object grant is checked before any row is read. Without it a reader whose
// role grants no activity read still reaches a brief describing a meeting, its
// attendees and their commitments — through a door every sibling read refuses.
func TestMeetingBriefRefusesACallerWithNoActivityGrant(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	meeting := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'Expansion review', $2,
		        'manual', 'human:x')`, roomTomorrow)
	LinkActivity(t, owner, meeting, "person", mine)

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
	meeting := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'Expansion review', $2,
		        'manual', 'human:x')`, roomTomorrow)
	LinkActivity(t, owner, meeting, "person", mine)

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
// linked to. A meeting linked only to another rep's capture-private contact is
// therefore outside this caller's scope.
func TestMeetingBriefRefusesAMeetingItCannotReach(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	e.MakeCapturePrivate(t, "person", theirs, e.Rep3)
	meeting := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'Their review', $2,
		        'manual', 'human:x')`, roomTomorrow)
	LinkActivity(t, owner, meeting, "person", theirs)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)

	_, err := meetingBriefService(e).Get(rep, meeting)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("brief on a meeting linked only to a capture-private contact → %v, want ErrNotFound", err)
	}
}

// An attendee's "last touch" is read from ACTIVITIES, so it takes the activity
// row scope like every other activity read on this page.
//
// The two tables diverge legitimately: participants are resolved from message
// headers, links are supplied by the connector. So a conversation can name this
// caller's attendee as a participant while being linked only to another rep's
// capture-private contact — and an unscoped sub-select would report when that
// conversation happened, disclosing both its timing and that it exists at all.
func TestMeetingBriefDoesNotReportALastTouchTheCallerCannotRead(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	attendee := e.SeedPerson(t, "Ana Roth", &e.Rep1)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	e.MakeCapturePrivate(t, "person", theirs, e.Rep3)

	meeting := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'Expansion review', $2,
		        'manual', 'human:x')`, roomTomorrow)
	LinkActivity(t, owner, meeting, "person", attendee)
	seatInRoom(t, owner, e.WS, meeting, attendee)

	// A conversation this caller may not read, which nonetheless names their
	// attendee as a participant.
	hidden := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'email', 'Cc: budget', $2,
		        'manual', 'human:x')`, roomAgo(3*24*time.Hour))
	LinkActivity(t, owner, hidden, "person", theirs)
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
		`INSERT INTO activity_participant (activity_id, role, person_id)
		 VALUES ($1, 'attendee', $2)`, activity, person); err != nil {
		t.Fatalf("seating a participant: %v", err)
	}
}

// roomPermsWithProject is the bounded rep plus the project grant. A brief that
// names an engagement is TWO reads, and the project half needs its own object
// grant — roomPerms deliberately carries none, which is what
// TestMeetingBriefWithholdsTheEngagementFromACallerWithNoProjectGrant proves.
func roomPermsWithProject() principal.Permissions {
	perms := roomPerms
	perms.Objects = map[string]principal.ObjectGrant{"project": {Read: true}}
	for object, grant := range roomPerms.Objects {
		perms.Objects[object] = grant
	}
	return perms
}

// The brief's project lines are rendered from a lateral join and a correlated
// sub-select, and both reference an alias declared elsewhere in the same FROM
// clause. Unit tests over the section writers cannot see any of that: they are
// handed an Input that already holds a project. Only a real query proves the
// SQL puts one there.
func TestMeetingBriefNamesTheEngagementItIsFiledUnder(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	org := e.SeedOrg(t, "Northwind", &e.Rep1)
	attendee := e.SeedPerson(t, "Ana Roth", &e.Rep1)

	project := SeedIDRow(t, owner, `INSERT INTO project (id, owner_id, name, key, phase, organization_id, source, captured_by)
		VALUES ($1, $2, 'ERP rollout', 'ERP-27', 'delivering', $3, 'manual', 'human:x')`, e.Rep1, org)

	meeting := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'Cutover review', $2, 'manual', 'human:x')`, roomTomorrow)
	LinkActivity(t, owner, meeting, "person", attendee)
	seatInRoom(t, owner, e.WS, meeting, attendee)
	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, project_id)
		VALUES ($1, 'project', $2)`, meeting, project)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPermsWithProject())
	brief, err := meetingBriefService(e).Get(rep, meeting)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	var header, goal string
	for _, section := range brief.Sections {
		for _, sentence := range section.Sentences {
			switch section.Kind {
			case "header":
				header += sentence.Text + "\n"
			case "goal":
				goal += sentence.Text + "\n"
			}
		}
	}
	if !strings.Contains(header, "ERP rollout") || !strings.Contains(header, "ERP-27") {
		t.Errorf("header = %q, want the engagement and its key", header)
	}
	// The room has no deal and no open promise, so before the project arm the
	// goal section was absent entirely — which is the failure this fixes.
	if !strings.Contains(goal, "ERP rollout") {
		t.Errorf("goal = %q, want the engagement's own next step", goal)
	}
}

// A meeting filed under one engagement must not report a last touch measured
// against another. This is the number a reader trusts most, and scoping the
// deal while leaving the attendee sub-select alone is the predicted mistake.
func TestMeetingBriefCountsNoLastTouchFromAnotherEngagement(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	org := e.SeedOrg(t, "Northwind", &e.Rep1)
	attendee := e.SeedPerson(t, "Ana Roth", &e.Rep1)

	newProject := func(name, key string) ids.UUID {
		return SeedIDRow(t, owner, `INSERT INTO project (id, owner_id, name, key, organization_id, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, 'manual', 'human:x')`, e.Rep1, name, key, org)
	}
	erp := newProject("ERP rollout", "ERP-27")
	migration := newProject("Datacentre migration", "DC-4")

	meeting := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'Cutover review', $2, 'manual', 'human:x')`, roomTomorrow)
	LinkActivity(t, owner, meeting, "person", attendee)
	seatInRoom(t, owner, e.WS, meeting, attendee)
	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, project_id)
		VALUES ($1, 'project', $2)`, meeting, erp)

	// The ONLY prior conversation with this attendee belongs to the other
	// engagement, so within this room's scope they have never been spoken to.
	other := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'email', 'Rack decommissioning', $2, 'manual', 'human:x')`, roomAgo(3*24*time.Hour))
	LinkActivity(t, owner, other, "person", attendee)
	seatInRoom(t, owner, e.WS, other, attendee)
	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, project_id)
		VALUES ($1, 'project', $2)`, other, migration)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPermsWithProject())
	brief, err := meetingBriefService(e).Get(rep, meeting)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
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
	if !strings.Contains(attendees, "first") {
		t.Errorf("attendees = %q; want Ana Roth flagged first-time — her only prior conversation belongs to the other engagement", attendees)
	}
}

// The project is a SECOND gate, and the two are different questions: row scope
// decides which projects a caller may see, the object grant decides whether
// they may see projects at all. Since projects became workspace-readable the
// row clause admits everyone, so without the grant check a caller who may open
// the meeting reads the engagement's name, key, phase and target date off it.
func TestMeetingBriefWithholdsTheEngagementFromACallerWithNoProjectGrant(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	org := e.SeedOrg(t, "Northwind", &e.Rep1)
	attendee := e.SeedPerson(t, "Ana Roth", &e.Rep1)
	project := SeedIDRow(t, owner, `INSERT INTO project (id, owner_id, name, key, phase, organization_id, source, captured_by)
		VALUES ($1, $2, 'ERP rollout', 'ERP-27', 'delivering', $3, 'manual', 'human:x')`, e.Rep1, org)

	meeting := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'Cutover review', $2, 'manual', 'human:x')`, roomTomorrow)
	LinkActivity(t, owner, meeting, "person", attendee)
	seatInRoom(t, owner, e.WS, meeting, attendee)
	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, project_id)
		VALUES ($1, 'project', $2)`, meeting, project)

	read := func(perms principal.Permissions) string {
		t.Helper()
		brief, err := meetingBriefService(e).Get(e.As(e.Rep1, []ids.UUID{e.Team1}, perms), meeting)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		var prose string
		for _, section := range brief.Sections {
			for _, sentence := range section.Sentences {
				prose += sentence.Text + "\n"
			}
		}
		return prose
	}

	// The admit case first. Three security tests in this repo once passed
	// against an authority that refused everyone, so a refusal test proves
	// nothing until the same fixture is shown to admit.
	if !strings.Contains(read(roomPermsWithProject()), "ERP rollout") {
		t.Fatal("a caller WITH the project grant cannot see the engagement, so the refusal below proves nothing")
	}

	// roomPerms itself carries no project grant.
	if prose := read(roomPerms); strings.Contains(prose, "ERP") {
		t.Errorf("the brief disclosed the engagement to a caller with no project grant: %q", prose)
	}
}
