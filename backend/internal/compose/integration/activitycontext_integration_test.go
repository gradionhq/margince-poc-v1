// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The activity anchor against a real database: a captured meeting is
// dereferenced to the records it is about, and the prep is built around one of
// them.
//
// The claim that matters most here is a REFUSAL. An event is readable when ANY
// record it links to is readable (the activity link-walk scope), so a meeting
// that touches two teams' deals is visible to both reps — and each must be
// prepped against their own. Dereferencing widens context; it must never widen
// authority.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// prepFor walks the context for one activity as the given caller.
func prepFor(ctx context.Context, t *testing.T, e *SearchEnv, activity ids.UUID) (retrieval.Context, error) {
	t.Helper()
	return search.NewRetriever(e.Store, nil).AssembleContext(ctx,
		datasource.EntityRef{Type: datasource.EntityActivity, ID: activity},
		retrieval.AssembleOptions{MaxItems: 5})
}

// summariesIn returns one section's item summaries, and nil when the walk did
// not emit the section at all — the two read the same to a caller and this
// suite asserts on both the same way.
func summariesIn(assembled retrieval.Context, section string) []string {
	var out []string
	for _, sec := range assembled.Sections {
		if sec.Name != section {
			continue
		}
		for _, item := range sec.Items {
			out = append(out, item.Summary)
		}
	}
	return out
}

func refsIn(assembled retrieval.Context, section string) []datasource.EntityRef {
	var out []datasource.EntityRef
	for _, sec := range assembled.Sections {
		if sec.Name != section {
			continue
		}
		for _, item := range sec.Items {
			out = append(out, item.Ref)
		}
	}
	return out
}

func assertPreparedFor(t *testing.T, assembled retrieval.Context, want datasource.EntityRef) {
	t.Helper()
	got := refsIn(assembled, "prepared_for")
	if len(got) != 1 {
		t.Fatalf("prepared_for = %+v, want exactly the one subject %+v", got, want)
	}
	if got[0] != want {
		t.Fatalf("prepared_for = %+v, want %+v", got[0], want)
	}
}

// meetingFixture is one workspace's calendar event and everything it can name.
type meetingFixture struct {
	pipeline, stage        ids.UUID
	rep1Org, rep1Deal      ids.UUID
	rep3Org, rep3Project   ids.UUID
	organizer, otherPerson ids.UUID
}

func seedMeetingFixture(t *testing.T, e *SearchEnv) meetingFixture {
	t.Helper()
	var f meetingFixture
	f.pipeline = e.SeedID(t, `INSERT INTO pipeline (id, name, is_default, position)
		VALUES ($1, 'Sales', true, 0)`)
	f.stage = e.SeedID(t, `INSERT INTO stage (id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, 'Qualify', 0, 'open', 10)`, f.pipeline)

	// Seeded rep3-first on purpose: ids are time-ordered, so the record the
	// caller may NOT see sorts ahead of the one they may. A walk that skipped
	// the per-subject visibility probe would prep against it. Every shareable
	// record type is read by every seat (platform/auth tableclass.go), so
	// capture privacy is what hides a record here: the other team's
	// organization is 'owner'-visible and only Rep3 reads it. The project
	// hanging off it is visible to everyone and is the neighbourhood, not the
	// hidden record.
	f.rep3Org = e.SeedID(t, `INSERT INTO organization (id, owner_id, display_name, visibility, source, captured_by)
		VALUES ($1, $2, 'Other Team GmbH', 'owner', 'manual', 'human:x')`, e.Rep3)
	f.rep3Project = e.SeedID(t, `INSERT INTO project (id, owner_id, name, organization_id, source, captured_by)
		VALUES ($1, $2, 'Other Team Rollout', $3, 'manual', 'human:x')`, e.Rep3, f.rep3Org)
	f.rep1Org = e.SeedID(t, `INSERT INTO organization (id, owner_id, display_name, source, captured_by)
		VALUES ($1, $2, 'Turbinenbau AG', 'manual', 'human:x')`, e.Rep1)
	f.rep1Deal = e.SeedID(t, `INSERT INTO deal (id, owner_id, name, pipeline_id, stage_id, organization_id, source, captured_by)
		VALUES ($1, $2, 'Turbinenbau Renewal', $3, $4, $5, 'manual', 'human:x')`,
		e.Rep1, f.pipeline, f.stage, f.rep1Org)
	f.organizer = e.SeedID(t, `INSERT INTO person (id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, 'Annegret Weiss', 'manual', 'human:x')`, e.Rep1)
	f.otherPerson = e.SeedID(t, `INSERT INTO person (id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, 'Bernhard Klein', 'manual', 'human:x')`, e.Rep1)
	return f
}

// seedMeeting records one captured calendar event.
func seedMeeting(t *testing.T, e *SearchEnv, subject string) ids.UUID {
	t.Helper()
	return e.SeedID(t, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', $2, now(), 'connector', 'connector:gcal')`, subject)
}

func linkMeeting(t *testing.T, e *SearchEnv, meeting ids.UUID, entityType, column string, target ids.UUID) {
	t.Helper()
	e.SeedID(t, `INSERT INTO activity_link (id, activity_id, entity_type, `+column+`)
		VALUES ($1, $2, $3, $4)`, meeting, entityType, target)
}

func addParty(t *testing.T, e *SearchEnv, meeting ids.UUID, role string, person *ids.UUID, address string) {
	t.Helper()
	e.SeedID(t, `INSERT INTO activity_participant (id, activity_id, role, person_id, address)
		VALUES ($1, $2, $3, $4, $5)`, meeting, role, person, address)
}

// The headline case: a meeting that names a deal is prepped against the deal,
// and everything else it named is reported rather than dropped.
func TestAMeetingPrepsAgainstItsLinkedDealAndNamesTheRest(t *testing.T) {
	e := SetupSearch(t)
	f := seedMeetingFixture(t, e)
	meeting := seedMeeting(t, e, "Renewal review")
	linkMeeting(t, e, meeting, "deal", "deal_id", f.rep1Deal)
	linkMeeting(t, e, meeting, "organization", "organization_id", f.rep1Org)
	addParty(t, e, meeting, "organizer", &f.organizer, "annegret@turbinenbau.example")
	addParty(t, e, meeting, "attendee", nil, "unknown@turbinenbau.example")

	assembled, err := prepFor(e.Admin(), t, e, meeting)
	if err != nil {
		t.Fatalf("preparing for the meeting: %v", err)
	}

	assertPreparedFor(t, assembled, datasource.EntityRef{Type: datasource.EntityDeal, ID: f.rep1Deal})
	also := refsIn(assembled, "also_present")
	for _, want := range []datasource.EntityRef{
		{Type: datasource.EntityOrganization, ID: f.rep1Org},
		{Type: datasource.EntityPerson, ID: f.organizer},
	} {
		if !containsRef(also, want) {
			t.Errorf("also_present = %+v, want it to name %+v — a subject the prep did not "+
				"anchor on is reported, not dropped", also, want)
		}
	}

	unresolved := summariesIn(assembled, "unresolved_attendees")
	if len(unresolved) != 1 || !strings.Contains(unresolved[0], "unknown@turbinenbau.example") {
		t.Errorf("unresolved_attendees = %v, want the one address that matched no record", unresolved)
	}
	// The organizer resolved to a person, so their address is a subject rather
	// than an unmatched attendee — reporting it in both places would read as
	// two people in the room.
	for _, summary := range unresolved {
		if strings.Contains(summary, "annegret@turbinenbau.example") {
			t.Errorf("unresolved_attendees names %q, an address that DID resolve", summary)
		}
	}

	// The profile is the event, not the deal — a prep opens with the meeting
	// it is for — and the walk that follows is the deal's.
	profile := refsIn(assembled, "profile")
	if len(profile) != 1 || profile[0].Type != datasource.EntityActivity || profile[0].ID != meeting {
		t.Fatalf("profile = %+v, want the event %s", profile, meeting)
	}
	if len(refsIn(assembled, "recent_touches")) == 0 {
		t.Errorf("the prep carries no recent_touches, so the walk around the deal did not run: %+v",
			sectionNames(assembled))
	}
}

// With no links at all, the people on the invitation are the subjects — and
// the one who convened it is the subject the prep is built around.
func TestAMeetingWithOnlyAttendeesPrepsAgainstTheOrganizer(t *testing.T) {
	e := SetupSearch(t)
	f := seedMeetingFixture(t, e)
	meeting := seedMeeting(t, e, "Intro call")
	// The attendee is seeded first and owns the lower id, so an ordering that
	// ignored the role would pick them.
	addParty(t, e, meeting, "attendee", &f.otherPerson, "bernhard@turbinenbau.example")
	addParty(t, e, meeting, "organizer", &f.organizer, "annegret@turbinenbau.example")

	assembled, err := prepFor(e.Admin(), t, e, meeting)
	if err != nil {
		t.Fatalf("preparing for the meeting: %v", err)
	}
	assertPreparedFor(t, assembled, datasource.EntityRef{Type: datasource.EntityPerson, ID: f.organizer})
	if also := refsIn(assembled, "also_present"); !containsRef(also,
		datasource.EntityRef{Type: datasource.EntityPerson, ID: f.otherPerson}) {
		t.Errorf("also_present = %+v, want the other attendee named", also)
	}
}

// The discovery call. A lead is an arm of activity_link like any other (core
// 0038), and it is the record an early-funnel meeting is most often linked to —
// so a prep against one names the lead rather than nothing, even though a lead
// has no timeline of its own to walk.
func TestAMeetingLinkedOnlyToALeadPrepsAgainstTheLead(t *testing.T) {
	e := SetupSearch(t)
	seedMeetingFixture(t, e)
	lead := e.SeedID(t, `INSERT INTO lead (id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, 'Clara Vogt', 'manual', 'human:x')`, e.Rep1)
	meeting := seedMeeting(t, e, "Discovery call")
	linkMeeting(t, e, meeting, "lead", "lead_id", lead)

	assembled, err := prepFor(e.Admin(), t, e, meeting)
	if err != nil {
		t.Fatalf("preparing for the meeting: %v", err)
	}
	assertPreparedFor(t, assembled, datasource.EntityRef{Type: datasource.EntityLead, ID: lead})
	// A lead carries no activity_link neighborhood the record walk reads, so
	// the prep is honestly the event and the lead — never the 500 an unwalkable
	// subject would raise.
	for _, section := range assembled.Sections {
		if section.Name == "recent_touches" || section.Name == "open_tasks" {
			t.Errorf("a lead subject produced a %q section, which a lead has no walk for", section.Name)
		}
	}
}

// An event this workspace holds no record for still answers, and the answer is
// who was on it. Silence would be the one response an agent cannot act on.
func TestAMeetingThatNamesNoRecordStillNamesWhoWasOnIt(t *testing.T) {
	e := SetupSearch(t)
	seedMeetingFixture(t, e)
	meeting := seedMeeting(t, e, "Cold intro")
	addParty(t, e, meeting, "organizer", nil, "someone@elsewhere.example")

	assembled, err := prepFor(e.Admin(), t, e, meeting)
	if err != nil {
		t.Fatalf("preparing for the meeting: %v", err)
	}
	if got := refsIn(assembled, "prepared_for"); len(got) != 0 {
		t.Errorf("prepared_for = %+v, want nothing — the event names no record we hold", got)
	}
	unresolved := summariesIn(assembled, "unresolved_attendees")
	if len(unresolved) != 1 || !strings.Contains(unresolved[0], "someone@elsewhere.example") {
		t.Fatalf("unresolved_attendees = %v, want the one address on the invitation", unresolved)
	}
	if !strings.Contains(unresolved[0], "organizer") {
		t.Errorf("unresolved_attendees = %q, want the part they played named alongside the address",
			unresolved[0])
	}
}

// The refusal, and the two halves it has. A meeting spanning two teams is
// readable by both reps — the activity link-walk scope is an ANY-link rule —
// and neither may learn anything about the other team's records through it.
func TestAMeetingNeverDisclosesTheRecordBehindALinkTheCallerCannotSee(t *testing.T) {
	// A record the caller cannot see that would NOT have been the subject
	// anyway: it must be absent, not merely unchosen. This is the half a
	// prepared_for assertion cannot reach — an unprobed walk reports it under
	// also_present, where it reads as another company in the room.
	t.Run("a hidden record is not reported alongside the subject", func(t *testing.T) {
		e := SetupSearch(t)
		f := seedMeetingFixture(t, e)
		meeting := seedMeeting(t, e, "Joint review")
		linkMeeting(t, e, meeting, "deal", "deal_id", f.rep1Deal)
		linkMeeting(t, e, meeting, "organization", "organization_id", f.rep3Org)

		assembled, err := prepFor(e.AsTeamRep(e.Rep1, e.Team1), t, e, meeting)
		if err != nil {
			t.Fatalf("preparing for the meeting: %v", err)
		}
		assertPreparedFor(t, assembled, datasource.EntityRef{Type: datasource.EntityDeal, ID: f.rep1Deal})
		assertAbsent(t, assembled, datasource.EntityRef{Type: datasource.EntityOrganization, ID: f.rep3Org})
	})

	// A record the caller cannot see that WOULD have been the subject (an
	// account outranks a single contact in the subject tiers): the prep is
	// built around the one they can, not refused and not built around the one
	// they cannot.
	t.Run("a hidden record is never the subject", func(t *testing.T) {
		e := SetupSearch(t)
		f := seedMeetingFixture(t, e)
		meeting := seedMeeting(t, e, "Joint review")
		linkMeeting(t, e, meeting, "organization", "organization_id", f.rep3Org)
		linkMeeting(t, e, meeting, "person", "person_id", f.organizer)

		// The control: the capture's own owner preps against the account.
		theirs, err := prepFor(e.teamRepWhoReadsProjects(e.Rep3, e.Team2), t, e, meeting)
		if err != nil {
			t.Fatalf("preparing for the meeting as the capture's owner: %v", err)
		}
		assertPreparedFor(t, theirs, datasource.EntityRef{Type: datasource.EntityOrganization, ID: f.rep3Org})

		assembled, err := prepFor(e.teamRepWhoReadsProjects(e.Rep1, e.Team1), t, e, meeting)
		if err != nil {
			t.Fatalf("preparing for the meeting: %v", err)
		}
		assertPreparedFor(t, assembled, datasource.EntityRef{Type: datasource.EntityPerson, ID: f.organizer})
		assertAbsent(t, assembled, datasource.EntityRef{Type: datasource.EntityOrganization, ID: f.rep3Org})
	})
}

// A party matched to a person the caller cannot see is neither a subject nor
// an unmatched address. The second half is the one a ref sweep cannot catch:
// reclassifying them as "unresolved" would put their email in the answer as
// free text, disclosing through a summary exactly what the row scope withheld.
func TestAMeetingNeverDisclosesAnAttendeeTheCallerCannotSee(t *testing.T) {
	e := SetupSearch(t)
	f := seedMeetingFixture(t, e)
	// Contacts are readable by every seat, so the hidden attendee is a
	// capture-private contact of the other team's rep.
	hidden := e.SeedID(t, `INSERT INTO person (id, owner_id, full_name, visibility, source, captured_by)
		VALUES ($1, $2, 'Dieter Fremd', 'owner', 'manual', 'human:x')`, e.Rep3)
	meeting := seedMeeting(t, e, "Joint review")
	linkMeeting(t, e, meeting, "deal", "deal_id", f.rep1Deal)
	addParty(t, e, meeting, "organizer", &hidden, "dieter@othertteam.example")

	assembled, err := prepFor(e.AsTeamRep(e.Rep1, e.Team1), t, e, meeting)
	if err != nil {
		t.Fatalf("preparing for the meeting: %v", err)
	}
	assertPreparedFor(t, assembled, datasource.EntityRef{Type: datasource.EntityDeal, ID: f.rep1Deal})
	assertAbsent(t, assembled, datasource.EntityRef{Type: datasource.EntityPerson, ID: hidden})
	assertNoTextAnywhere(t, assembled, "dieter@othertteam.example", "Dieter Fremd")
}

// A colleague on the invitation resolved to a member, so they are not an
// unmatched address either — reporting them would tell an agent to go resolve
// someone who already works here.
func TestAColleagueOnTheInvitationIsNotAnUnresolvedAttendee(t *testing.T) {
	e := SetupSearch(t)
	f := seedMeetingFixture(t, e)
	meeting := seedMeeting(t, e, "Internal prep")
	linkMeeting(t, e, meeting, "deal", "deal_id", f.rep1Deal)
	e.SeedID(t, `INSERT INTO activity_participant (id, activity_id, role, user_id, address)
		VALUES ($1, $2, 'organizer', $3, 'rep0@search.test')`, meeting, e.Rep1)

	assembled, err := prepFor(e.Admin(), t, e, meeting)
	if err != nil {
		t.Fatalf("preparing for the meeting: %v", err)
	}
	assertNoTextAnywhere(t, assembled, "rep0@search.test")
}

// The bounded participant window is cut by ROLE, not by id. With more
// attendees than one leg of the walk reads, an organizer holding the highest
// id must still be the subject — cutting by id would drop them before the
// precedence ever saw them, and the prep would be built around whichever
// attendee happened to sort first.
func TestTheOrganizerSurvivesAnOversizedInvitation(t *testing.T) {
	e := SetupSearch(t)
	seedMeetingFixture(t, e)
	meeting := seedMeeting(t, e, "All hands with the customer")
	// 60 attendees, past the 50-row window one leg of the walk reads. They are
	// seeded FIRST, so every one of them holds a lower id than the organizer.
	if _, err := e.Owner.Exec(context.Background(), `
		WITH room AS (
		    INSERT INTO person (owner_id, full_name, source, captured_by)
		    SELECT $1, 'Attendee ' || n, 'manual', 'human:x' FROM generate_series(1, 60) n
		    RETURNING id
		)
		INSERT INTO activity_participant (activity_id, role, person_id)
		SELECT $2, 'attendee', id FROM room`, e.Rep1, meeting); err != nil {
		t.Fatalf("seeding the oversized invitation: %v", err)
	}
	organizer := e.SeedID(t, `INSERT INTO person (id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, 'Zoe Organizer', 'manual', 'human:x')`, e.Rep1)
	addParty(t, e, meeting, "organizer", &organizer, "zoe@turbinenbau.example")

	assembled, err := prepFor(e.Admin(), t, e, meeting)
	if err != nil {
		t.Fatalf("preparing for the meeting: %v", err)
	}
	assertPreparedFor(t, assembled, datasource.EntityRef{Type: datasource.EntityPerson, ID: organizer})
	// And the room is bounded like every other section rather than exported.
	if also := refsIn(assembled, "also_present"); len(also) > 5 {
		t.Errorf("also_present carries %d items at max_items=5 — the section is a window, not an export",
			len(also))
	}
}

// readerOf is a caller unbounded by row scope who may read only the named
// record types — the one principal shape that isolates OBJECT RBAC from row
// scope, because an unbounded caller skips the row probe entirely. Every other
// principal in this suite may read every type, so nothing else here can tell
// the two gates apart.
func (e *SearchEnv) readerOf(objects ...string) context.Context {
	grants := make(map[string]principal.ObjectGrant, len(objects))
	for _, object := range objects {
		grants[object] = principal.ObjectGrant{Read: true}
	}
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{Objects: grants, RowScope: principal.RowScopeAll},
	})
}

// Row scope answers WHICH events; object RBAC answers whether this caller may
// read events at all. A caller holding no activity grant is denied the type
// rather than handed the ones their row scope would have admitted.
func TestAnEventIsRefusedToACallerWithNoActivityGrant(t *testing.T) {
	e := SetupSearch(t)
	f := seedMeetingFixture(t, e)
	meeting := seedMeeting(t, e, "Renewal review")
	linkMeeting(t, e, meeting, "deal", "deal_id", f.rep1Deal)

	_, err := prepFor(e.readerOf(objPerson, objOrg, objDeal), t, e, meeting)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("preparing without an activity grant = %v, want permission denied", err)
	}
}

// A dereferenced subject is context the caller never asked for by name, so a
// record TYPE they hold no grant on is absent rather than a 403 — and absent
// means the prep moves to the next tier it may read, not that it names the
// deal anyway.
func TestASubjectTypeTheCallerMayNotReadIsNeverNamed(t *testing.T) {
	e := SetupSearch(t)
	f := seedMeetingFixture(t, e)
	meeting := seedMeeting(t, e, "Renewal review")
	linkMeeting(t, e, meeting, "deal", "deal_id", f.rep1Deal)
	linkMeeting(t, e, meeting, "organization", "organization_id", f.rep1Org)

	assembled, err := prepFor(e.readerOf(objActivity, objPerson, objOrg), t, e, meeting)
	if err != nil {
		t.Fatalf("preparing for the meeting: %v", err)
	}
	assertPreparedFor(t, assembled, datasource.EntityRef{Type: datasource.EntityOrganization, ID: f.rep1Org})
	assertAbsent(t, assembled, datasource.EntityRef{Type: datasource.EntityDeal, ID: f.rep1Deal})
	assertNoTextAnywhere(t, assembled, "Turbinenbau Renewal")
}

// assertAbsent holds the whole assembled picture against one ref, not just the
// section it would most obviously appear in: the walk around the subject can
// reach a record through hop 2 as easily as the dereference can name it.
func assertAbsent(t *testing.T, assembled retrieval.Context, hidden datasource.EntityRef) {
	t.Helper()
	for _, section := range assembled.Sections {
		for _, item := range section.Items {
			if item.Ref == hidden {
				t.Errorf("section %q names %s %s, which the caller may not see",
					section.Name, hidden.Type, hidden.ID)
			}
		}
	}
}

// assertNoTextAnywhere sweeps the summaries and the evidence, which a ref
// comparison cannot reach. An address or a name has no id of its own, so the
// only way it can leak is as prose — and prose is exactly what an assembled
// context is made of.
func assertNoTextAnywhere(t *testing.T, assembled retrieval.Context, forbidden ...string) {
	t.Helper()
	for _, section := range assembled.Sections {
		for _, item := range section.Items {
			text := []string{item.Summary}
			for _, ev := range item.Evidence {
				text = append(text, ev.Snippet, ev.Source)
			}
			for _, needle := range forbidden {
				for _, haystack := range text {
					if strings.Contains(haystack, needle) {
						t.Errorf("section %q carries %q in %q, which the caller may not see",
							section.Name, needle, haystack)
					}
				}
			}
		}
	}
}

// An event reachable through no readable link is not found — the same answer
// any other anchor gives, never a leak of who was in someone else's meeting.
func TestAnEventOutsideTheCallersScopeIsNotFound(t *testing.T) {
	e := SetupSearch(t)
	f := seedMeetingFixture(t, e)
	meeting := seedMeeting(t, e, "Other team only")
	// The one link is to a capture-private account: every other record type
	// is read by every seat (platform/auth tableclass.go), so this is what an
	// event nobody else may reach looks like now.
	linkMeeting(t, e, meeting, "organization", "organization_id", f.rep3Org)

	if _, err := prepFor(e.teamRepWhoReadsProjects(e.Rep3, e.Team2), t, e, meeting); err != nil {
		t.Fatalf("preparing for the meeting as the capture's own owner: %v", err)
	}
	if _, err := prepFor(e.teamRepWhoReadsProjects(e.Rep1, e.Team1), t, e, meeting); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("preparing for another team's meeting = %v, want not-found", err)
	}
}

// teamRepWhoReadsProjects is AsTeamRep with the project read grant added: the
// search fixture's grant vocabulary stops at the five searchable types, and a
// suite whose hidden record is a project must hold the grant so that what
// hides the row is the team row scope and not object RBAC.
func (e *SearchEnv) teamRepWhoReadsProjects(user, team ids.UUID) context.Context {
	grants := searchReadGrants()
	grants["project"] = principal.ObjectGrant{Read: true}
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs:     []ids.UUID{team},
		Permissions: principal.Permissions{Objects: grants, RowScope: principal.RowScopeTeam},
	})
}

// An archived event answers the same not-found, so a prep never re-serves a
// meeting every other read has stopped returning.
func TestAnArchivedEventIsNotFound(t *testing.T) {
	e := SetupSearch(t)
	seedMeetingFixture(t, e)
	meeting := seedMeeting(t, e, "Cancelled review")
	if _, err := e.Owner.Exec(context.Background(),
		`UPDATE activity SET archived_at = now() WHERE id = $1`, meeting); err != nil {
		t.Fatalf("archiving the event: %v", err)
	}
	if _, err := prepFor(e.Admin(), t, e, meeting); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("preparing for an archived meeting = %v, want not-found", err)
	}
}

// An event id nobody wrote answers not-found rather than an empty picture that
// reads as a meeting with nothing in it.
func TestAnUnknownEventIsNotFound(t *testing.T) {
	e := SetupSearch(t)
	if _, err := prepFor(e.Admin(), t, e, ids.NewV7()); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("preparing for an event that does not exist = %v, want not-found", err)
	}
}

func containsRef(refs []datasource.EntityRef, want datasource.EntityRef) bool {
	for _, ref := range refs {
		if ref == want {
			return true
		}
	}
	return false
}

func sectionNames(assembled retrieval.Context) []string {
	out := make([]string, 0, len(assembled.Sections))
	for _, section := range assembled.Sections {
		out = append(out, section.Name)
	}
	return out
}
