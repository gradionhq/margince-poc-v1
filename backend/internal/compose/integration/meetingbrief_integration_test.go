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
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func meetingBriefService(e *Env) *meetingbrief.Service {
	view := person360.NewService(e.Pool, e.People, e.Deals, consent.NewStore(e.DB()),
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

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
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

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
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
	if !strings.Contains(read(roomPerms), "ERP rollout") {
		t.Fatal("a caller WITH the project grant cannot see the engagement, so the refusal below proves nothing")
	}

	if prose := read(withoutGrant(roomPerms, "project")); strings.Contains(prose, "ERP") {
		t.Errorf("the brief disclosed the engagement to a caller with no project grant: %q", prose)
	}
}

// "This room" is the PEOPLE, not the calendar entry. A recurring series that
// changed its title is still the same conversation; two unrelated meetings on
// one account are not. Only a real query proves the overlap rule.
func TestMeetingBriefRecallsWhenThisRoomLastMet(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	ours := e.SeedPerson(t, "Ana Roth", &e.Rep1)
	stranger := e.SeedPerson(t, "Someone Else", &e.Rep1)

	newMeeting := func(subject string, when time.Duration, who ids.UUID) {
		id := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
			VALUES ($1, 'meeting', $2, $3, 'manual', 'human:x')`, subject, roomAgo(when))
		LinkActivity(t, owner, id, "person", who)
		seatInRoom(t, owner, e.WS, id, who)
	}
	newMeeting("Kickoff", 30*24*time.Hour, ours)
	// A meeting on the same account with nobody from this room in it. It must
	// not be recalled: it is a different conversation.
	newMeeting("Unrelated review", 5*24*time.Hour, stranger)

	meeting := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'Cutover review', $2, 'manual', 'human:x')`, roomTomorrow)
	LinkActivity(t, owner, meeting, "person", ours)
	seatInRoom(t, owner, e.WS, meeting, ours)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	brief, err := meetingBriefService(e).Get(rep, meeting)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var recalled string
	for _, section := range brief.Sections {
		if section.Kind != "company_context" {
			continue
		}
		for _, sentence := range section.Sentences {
			recalled += sentence.Text + "\n"
		}
	}
	if !strings.Contains(recalled, "Kickoff") {
		t.Errorf("recalled = %q, want the earlier meeting with this same room", recalled)
	}
	if strings.Contains(recalled, "Unrelated") {
		t.Errorf("recalled = %q, but nobody from this room was in that meeting", recalled)
	}
}

// A brief scoped to one engagement must not reach into the other for its
// history — the same rule the rest of the page runs.
func TestMeetingBriefRecallsNoMeetingFromAnotherEngagement(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	org := e.SeedOrg(t, "Northwind", &e.Rep1)
	ours := e.SeedPerson(t, "Ana Roth", &e.Rep1)

	newProject := func(name, key string) ids.UUID {
		return SeedIDRow(t, owner, `INSERT INTO project (id, owner_id, name, key, organization_id, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, 'manual', 'human:x')`, e.Rep1, name, key, org)
	}
	erp := newProject("ERP rollout", "ERP-27")
	migration := newProject("Datacentre migration", "DC-4")

	fileUnder := func(activity, project ids.UUID) {
		e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, project_id)
			VALUES ($1, 'project', $2)`, activity, project)
	}
	newMeeting := func(subject string, when time.Duration) ids.UUID {
		id := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
			VALUES ($1, 'meeting', $2, $3, 'manual', 'human:x')`, subject, roomAgo(when))
		LinkActivity(t, owner, id, "person", ours)
		seatInRoom(t, owner, e.WS, id, ours)
		return id
	}

	fileUnder(newMeeting("ERP kickoff", 30*24*time.Hour), erp)
	fileUnder(newMeeting("Rack walkthrough", 5*24*time.Hour), migration)
	// Unfiled history stays: attribution is optional, so most of the record
	// carries no project and dropping it would erase the relationship.
	newMeeting("Quarterly catch-up", 10*24*time.Hour)

	meeting := newMeeting("Cutover review", -24*time.Hour)
	fileUnder(meeting, erp)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	brief, err := meetingBriefService(e).Get(rep, meeting)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var recalled string
	for _, section := range brief.Sections {
		if section.Kind != "company_context" {
			continue
		}
		for _, sentence := range section.Sentences {
			recalled += sentence.Text + "\n"
		}
	}
	// Three assertions, because the rule has three arms and an OR over two of
	// them would let a mutation that drops either one still pass.
	if strings.Contains(recalled, "Rack walkthrough") {
		t.Errorf("recalled = %q, but that meeting belongs to the other engagement", recalled)
	}
	if !strings.Contains(recalled, "ERP kickoff") {
		t.Errorf("recalled = %q, want this engagement's own earlier meeting", recalled)
	}
	if !strings.Contains(recalled, "Quarterly catch-up") {
		t.Errorf("recalled = %q, want the unfiled meeting too — attribution is optional, so most history carries no project", recalled)
	}
}

// Naming an earlier meeting means printing its SUBJECT, which is content
// rather than a marker. A reader who may DISCOVER that a conversation
// happened is not thereby entitled to read what it was called, so this
// section takes the content clause — the weaker discover clause is documented
// as covering the safe markers alone (a last-touch date, an open-task count).
func TestMeetingBriefRecallsNoSubjectItMayNotRead(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	ours := e.SeedPerson(t, "Ana Roth", &e.Rep1)

	// An earlier meeting our attendee sat in, whose author then limited it to
	// its participants. Rep1 is not one, so the row stays DISCOVERABLE to them
	// and its content does not — which is the exact gap between the two
	// clauses, and the only fixture that can tell them apart.
	when := roomAgo(6 * 24 * time.Hour)
	hidden, _, err := e.Activities.LogActivity(
		e.As(e.Rep3, []ids.UUID{e.Team2}, activityLifecyclePerms),
		activities.LogActivityInput{
			Kind: "meeting", Subject: strPtr("Board compensation review"),
			OccurredAt: &when, Source: "manual",
			Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: ours}},
		})
	if err != nil {
		t.Fatalf("log the earlier meeting: %v", err)
	}
	hiddenID := ids.From[ids.ActivityKind](ids.UUID(hidden.Id))
	seatInRoom(t, owner, e.WS, ids.UUID(hidden.Id), ours)
	if _, err := e.Activities.SetAudience(
		e.As(e.Rep3, []ids.UUID{e.Team2}, activityLifecyclePerms),
		hiddenID, activities.SetAudienceInput{Audience: "participants"}); err != nil {
		t.Fatalf("limit the earlier meeting: %v", err)
	}

	meeting := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'Cutover review', $2, 'manual', 'human:x')`, roomTomorrow)
	LinkActivity(t, owner, meeting, "person", ours)
	seatInRoom(t, owner, e.WS, meeting, ours)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	brief, err := meetingBriefService(e).Get(rep, meeting)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, section := range brief.Sections {
		for _, sentence := range section.Sentences {
			if strings.Contains(sentence.Text, "compensation") {
				t.Errorf("the brief disclosed a subject this caller may not read: %q", sentence.Text)
			}
		}
	}
}

// The commitments section is evidence from conversations, and a conversation
// on the account's OTHER engagement is the wrong evidence for this room: a
// meeting filed under one project does not report a promise made on the
// other project's mail, while a promise made on mail filed under no project
// still stands, the way every project-scoped read keeps the unfiled rows.
func TestMeetingBriefReportsNoCommitmentFromAnotherEngagement(t *testing.T) {
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
	mail := func(subject string, within *ids.UUID) ids.UUID {
		id := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
			VALUES ($1, 'email', $2, $3, 'manual', 'human:x')`, subject, roomAgo(2*24*time.Hour))
		LinkActivity(t, owner, id, "person", attendee)
		if within != nil {
			e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, project_id) VALUES ($1, 'project', $2)`, id, *within)
		}
		return id
	}
	meeting := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'Cutover review', $2, 'manual', 'human:x')`, roomTomorrow)
	LinkActivity(t, owner, meeting, "person", attendee)
	seatInRoom(t, owner, e.WS, meeting, attendee)
	e.WsExec(t, `INSERT INTO activity_link (activity_id, entity_type, project_id) VALUES ($1, 'project', $2)`, meeting, erp)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	promise := func(body string, source ids.UUID) {
		t.Helper()
		if _, err := e.People.RecordConversationClaim(rep, people.ClaimInput{
			PersonID: PersonIDOf(attendee), Kind: "commitment_theirs", Body: body,
			ActivityID: source, Quote: body, Source: "manual",
		}); err != nil {
			t.Fatalf("record claim %q: %v", body, err)
		}
	}
	promise("send the rack inventory", mail("Rack decommissioning", &migration))
	promise("confirm the cutover window", mail("ERP cutover plan", &erp))
	promise("forward the invoice", mail("Invoice question", nil))

	brief, err := meetingBriefService(e).Get(rep, meeting)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var commitments string
	for _, section := range brief.Sections {
		if section.Kind == crmcontracts.MeetingBriefSectionKindCommitments {
			for _, sentence := range section.Sentences {
				commitments += sentence.Text + "\n"
			}
		}
	}
	if !strings.Contains(commitments, "cutover window") || !strings.Contains(commitments, "invoice") {
		t.Fatalf("commitments = %q; want the promise on this engagement's mail and the one on unfiled mail", commitments)
	}
	if strings.Contains(commitments, "rack inventory") {
		t.Errorf("commitments = %q; reports a promise made on the other engagement's mail", commitments)
	}
}
