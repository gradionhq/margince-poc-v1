// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// The precedence that decides what a meeting prep is built around, proven
// without a database: which record a mixed set of links and attendees resolves
// to, and that the answer is the same every time it is asked.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// idOf mints a stable id from one digit, so a test can state the tie-break it
// expects instead of hoping a random pair sorts the right way.
func idOf(t *testing.T, digit string) ids.UUID {
	t.Helper()
	id, err := ids.Parse("0198f3a1-7c42-7e0b-9d51-2a6f4b8c1e0" + digit)
	if err != nil {
		t.Fatalf("parsing the fixture id: %v", err)
	}
	return id
}

// link and attendee build the two ways an event names a record, so a test case
// reads as the event it describes.
func link(t *testing.T, entity, digit string) activitySubject {
	t.Helper()
	return activitySubject{
		entityType: entity, id: idOf(t, digit), title: entity + digit,
		tier: subjectTier[entity], named: namedByLink, role: linkOnlyRole,
	}
}

func attendee(t *testing.T, role, digit string) activitySubject {
	t.Helper()
	person := string(datasource.EntityPerson)
	rank, ok := participantRoleRank[role]
	if !ok {
		rank = unrankedRole
	}
	return activitySubject{
		entityType: person, id: idOf(t, digit), title: role + digit,
		tier: subjectTier[person], named: namedByParticipant, role: rank,
	}
}

func titlesOf(subjects []activitySubject) []string {
	out := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		out = append(out, subject.title)
	}
	return out
}

func assertOrder(t *testing.T, got []activitySubject, want ...string) {
	t.Helper()
	titles := titlesOf(got)
	if len(titles) != len(want) {
		t.Fatalf("subjects = %v, want %v", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("subjects = %v, want %v", titles, want)
		}
	}
}

// The headline rule: the work outranks the account outranks the contact, so a
// meeting that names all three is prepared against the deal.
func TestTheWorkOutranksTheAccountOutranksTheContact(t *testing.T) {
	got := foldSubjects([]activitySubject{
		link(t, string(datasource.EntityPerson), "1"),
		link(t, string(datasource.EntityOrganization), "2"),
		link(t, string(datasource.EntityProject), "3"),
		link(t, string(datasource.EntityDeal), "4"),
	})
	assertOrder(t, got, "deal4", "project3", "organization2", "person1")
}

// A link is something capture ASSERTED about the record; a participant is
// something it matched from an address. Within one tier the assertion wins.
func TestALinkedPersonOutranksAMatchedAttendee(t *testing.T) {
	got := foldSubjects([]activitySubject{
		attendee(t, "organizer", "1"),
		link(t, string(datasource.EntityPerson), "2"),
	})
	assertOrder(t, got, "person2", "organizer1")
}

// Among the people the event merely matched, the party who convened it comes
// first — a prep built around whoever happens to sort first by id is a prep
// built around nobody in particular.
func TestTheOrganizerComesBeforeTheAttendees(t *testing.T) {
	got := foldSubjects([]activitySubject{
		attendee(t, "attendee", "1"),
		attendee(t, "cc", "2"),
		attendee(t, "organizer", "3"),
		attendee(t, "to", "4"),
	})
	assertOrder(t, got, "organizer3", "to4", "cc2", "attendee1")
}

// A role the map does not name still sorts, and sorts LAST among the
// participants: the vocabulary is a CHECK constraint that can gain a member,
// and a new one must not silently become the meeting's subject.
func TestARoleNobodyRankedSortsAfterEveryRankedOne(t *testing.T) {
	got := foldSubjects([]activitySubject{
		attendee(t, "chaired-the-thing", "1"),
		attendee(t, "attendee", "2"),
	})
	assertOrder(t, got, "attendee2", "chaired-the-thing1")
	for role, rank := range participantRoleRank {
		if rank >= unrankedRole {
			t.Errorf("role %q ranks %d, at or past the %d an unnamed role takes — "+
				"an unnamed role would outrank it", role, rank, unrankedRole)
		}
	}
}

// One record reached twice is ONE subject, at its best rank. A prep that lists
// the same account beside itself reads as two accounts.
func TestOneRecordNamedTwiceIsOneSubjectAtItsBestRank(t *testing.T) {
	person := string(datasource.EntityPerson)
	linked := link(t, person, "1")
	matched := attendee(t, "attendee", "1")
	matched.title = "same-person-as-attendee"

	for name, candidates := range map[string][]activitySubject{
		"link first":  {linked, matched},
		"match first": {matched, linked},
	} {
		t.Run(name, func(t *testing.T) {
			assertOrder(t, foldSubjects(candidates), linked.title)
		})
	}
}

// Two records the precedence cannot separate still come back in one order, so
// the same event prepares against the same record every time it is asked.
func TestSubjectsTiedOnEveryRankFallBackToTheId(t *testing.T) {
	organization := string(datasource.EntityOrganization)
	first, second := link(t, organization, "1"), link(t, organization, "2")
	assertOrder(t, foldSubjects([]activitySubject{second, first}), "organization1", "organization2")
	assertOrder(t, foldSubjects([]activitySubject{first, second}), "organization1", "organization2")
}

// An event that names nothing this workspace holds resolves to no subject, and
// that is an answer rather than an error.
func TestAnEventThatNamesNoRecordHasNoSubject(t *testing.T) {
	if got := foldSubjects(nil); len(got) != 0 {
		t.Fatalf("subjects = %v, want none", titlesOf(got))
	}
}

// Every arm of activity_link is a subject the precedence can place, and every
// subject it ranks is an arm — derived from the DDL's own enum rather than
// from a sibling list in Go, because that is how the lead arm went missing in
// the first draft: the hop-2 walk renders no related_leads section, so its own
// shorter list omits lead, and borrowing that list silently dropped exactly
// the discovery call a prep is most often for.
//
// A new arm with no tier would land on tier 0, ahead of the deal, by the
// silent default of a missing map entry.
func TestEverySubjectLinkArmIsRanked(t *testing.T) {
	declared := activityLinkEntityTypes(t)
	armed := make(map[string]bool, len(activityLinkArms))
	for _, arm := range activityLinkArms {
		armed[arm.entity] = true
		if _, ok := subjectTier[arm.entity]; !ok {
			t.Errorf("activityLinkArms reaches %q and subjectTier does not rank it, so it would "+
				"sort ahead of a deal as the meeting's subject", arm.entity)
		}
		if arm.title() == "" {
			t.Errorf("activityLinkArms reaches %q and no search branch renders it, so the arm's "+
				"read has no title column and would not run at all", arm.entity)
		}
	}
	for _, entity := range declared {
		if !armed[entity] {
			t.Errorf("activity_link admits entity_type %q and no activityLinkArms arm reads it — "+
				"an event linked to one prepares against nothing", entity)
		}
	}
	for entity := range subjectTier {
		if !armed[entity] {
			t.Errorf("subjectTier ranks %q, which no activityLinkArms arm produces — "+
				"the precedence names a subject an event cannot name", entity)
		}
	}
}

// activityLinkEntityTypes reads the live vocabulary off the migrations: the
// LAST activity_link_entity_type_check to be declared wins, which is the one a
// fresh database ends up with. Core migrations are zero-padded, so filename
// order is migration order.
func activityLinkEntityTypes(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "migrations", "core", "*.up.sql"))
	if err != nil {
		t.Fatalf("listing the core migrations: %v", err)
	}
	sort.Strings(files)
	check := regexp.MustCompile(`activity_link_entity_type_check[\s\S]*?entity_type IN \(([^)]*)\)`)
	create := regexp.MustCompile(`CREATE TABLE activity_link[\s\S]*?entity_type\s+text NOT NULL CHECK \(entity_type IN \(([^)]*)\)`)
	var latest string
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		for _, re := range []*regexp.Regexp{create, check} {
			if all := re.FindAllStringSubmatch(string(body), -1); len(all) > 0 {
				latest = all[len(all)-1][1]
			}
		}
	}
	if latest == "" {
		t.Fatal("no activity_link entity_type CHECK found in the core migrations; " +
			"the declaration this gate derives from has moved")
	}
	var out []string
	for _, quoted := range strings.Split(latest, ",") {
		out = append(out, strings.Trim(strings.TrimSpace(quoted), "'"))
	}
	return out
}
