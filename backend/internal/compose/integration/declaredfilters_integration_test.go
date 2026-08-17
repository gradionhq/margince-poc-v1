// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What the declared list filters answer, against real SQL.
//
// Each of these parameters was declared by the contract and dropped by its
// handler, so `?tag=vip` — or `?domain=`, or `?assignee_id=` — returned the
// UNFILTERED page with 200 OK. A unit test cannot tell that apart from a
// working filter, because the wrong answer is well-formed and the right one is
// a property of the query the database runs. So each filter is put to a set
// seeded to have a right answer and a wrong one: the row that matches, and the
// row a dropped filter would hand back with it.

import (
	"context"
	"slices"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// tagCurator is a principal that may author the tag vocabulary and apply it.
// The harness's admin fixture mirrors the real seed, which carries no tag
// grant, so the fixture asks for exactly the object it needs rather than
// widening the shared one.
func tagCurator(e *Env) context.Context {
	return e.As(ids.NewV7(), nil, principal.Permissions{
		Objects: map[string]principal.ObjectGrant{
			"tag": {Create: true, Read: true, Update: true},
		},
		RowScope: principal.RowScopeAll,
	})
}

func TestThePersonListNarrowsByTagName(t *testing.T) {
	e := Setup(t)
	tagged := e.SeedPerson(t, "Tagged Person", nil)
	e.SeedPerson(t, "Untagged Person", nil)

	tags := collections.NewStore(e.DB())
	curator := tagCurator(e)
	vip, err := tags.CreateTag(curator, "VIP", nil)
	if err != nil {
		t.Fatalf("creating the tag: %v", err)
	}
	if _, err := tags.ApplyTag(curator, vip.ID, "person", tagged); err != nil {
		t.Fatalf("applying the tag: %v", err)
	}

	// Folded on both sides: the vocabulary is unique under lower(name), so
	// the caller's capitalization decides nothing about which tag they named.
	for _, asked := range []string{"VIP", "vip", " Vip "} {
		page, _, err := e.People.ListPeople(e.Admin(), people.ListPeopleInput{Tag: &asked})
		if err != nil {
			t.Fatalf("listing people by tag %q: %v", asked, err)
		}
		if len(page) != 1 || ids.UUID(page[0].Id) != tagged {
			t.Fatalf("tag=%q returned %d people, want only the tagged one", asked, len(page))
		}
	}

	unknown := "no-such-tag"
	page, _, err := e.People.ListPeople(e.Admin(), people.ListPeopleInput{Tag: &unknown})
	if err != nil {
		t.Fatalf("listing people by an unused tag: %v", err)
	}
	if len(page) != 0 {
		t.Fatalf("a tag nobody carries returned %d people — a dropped filter answers the whole list", len(page))
	}
}

func TestTheOrganizationListNarrowsByDomain(t *testing.T) {
	e := Setup(t)
	held, err := e.People.CreateOrganization(e.Admin(), people.CreateOrganizationInput{
		DisplayName: "Acme", Domains: []people.OrgDomainInput{{Domain: "acme.example", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("seeding the account that holds the domain: %v", err)
	}
	if _, err := e.People.CreateOrganization(e.Admin(), people.CreateOrganizationInput{
		DisplayName: "Other", Domains: []people.OrgDomainInput{{Domain: "other.example", IsPrimary: true}},
	}); err != nil {
		t.Fatalf("seeding the account that does not: %v", err)
	}

	// Folded by the SAME parse the write path applies, so the lookup answers
	// for a caller who typed the domain out of an email signature and for one
	// who pasted the link out of a browser.
	for _, asked := range []string{"acme.example", "ACME.example", "https://www.acme.example/careers"} {
		page, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{Domain: &asked})
		if err != nil {
			t.Fatalf("listing organizations by domain %q: %v", asked, err)
		}
		if len(page) != 1 || page[0].Id != held.Id {
			t.Fatalf("domain=%q returned %d accounts, want only the one that lists it", asked, len(page))
		}
	}

	unheld := "nobody.example"
	page, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{Domain: &unheld})
	if err != nil {
		t.Fatalf("listing organizations by an unheld domain: %v", err)
	}
	if len(page) != 0 {
		t.Fatalf("a domain no account lists returned %d accounts — a dropped filter answers the whole list", len(page))
	}
}

// Archiving an account archives its domain rows in the same transaction, so a
// domain filter pinned to live rows could never answer the caller who asked
// for archived accounts BY domain: the page would come back empty, reading
// "no account ever held this". The two dials are one question.
func TestTheDomainFilterFindsAnArchivedAccountWhenAskedForOne(t *testing.T) {
	e := Setup(t)
	held, err := e.People.CreateOrganization(e.Admin(), people.CreateOrganizationInput{
		DisplayName: "Gone", Domains: []people.OrgDomainInput{{Domain: "gone.example", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("seeding the account: %v", err)
	}
	if _, err := e.People.ArchiveOrganization(e.Admin(), ids.From[ids.OrganizationKind](ids.UUID(held.Id))); err != nil {
		t.Fatalf("archiving the account: %v", err)
	}

	asked := "gone.example"
	live, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{Domain: &asked})
	if err != nil {
		t.Fatalf("listing live organizations by the domain: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("the live page returned %d accounts, want none — the holder is archived", len(live))
	}

	withArchived, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{
		Domain: &asked, IncludeArchived: true,
	})
	if err != nil {
		t.Fatalf("listing archived organizations by the domain: %v", err)
	}
	if len(withArchived) != 1 || withArchived[0].Id != held.Id {
		t.Fatalf("include_archived with a domain returned %d accounts, want the archived holder — a page that "+
			"cannot contain it answers 'nobody ever held this'", len(withArchived))
	}
}

func TestTheActivityListNarrowsToTheOpenTasksOneAssigneeHolds(t *testing.T) {
	e := Setup(t)
	// A fixed instant rather than the host clock: the assertion is about which
	// tasks the filter returns, and a due date that moves with the wall clock
	// makes a validation rule the test never meant to exercise part of it.
	due := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	mine := e.logTask(t, "Call the buyer", e.Rep1, due)
	closed := e.logTask(t, "Already handled", e.Rep1, due)
	e.logTask(t, "Someone else's", e.Rep3, due)
	if _, err := e.Activities.UpdateActivity(e.Admin(), ids.From[ids.ActivityKind](closed),
		activities.UpdateActivityInput{IsDone: boolPtr(true)}); err != nil {
		t.Fatalf("completing the task: %v", err)
	}

	assignee := ids.From[ids.UserKind](e.Rep1)
	page, _, err := e.Activities.ListActivities(e.Admin(), activities.ListActivitiesInput{AssigneeID: &assignee})
	if err != nil {
		t.Fatalf("listing activities by assignee: %v", err)
	}
	if len(page) != 1 || ids.UUID(page[0].Id) != mine {
		t.Fatalf("assignee_id returned %d activities, want the one open task that person holds "+
			"(a dropped filter answers every task in the workspace, a done one included)", len(page))
	}
}

// logTask writes one open task for an assignee through the store the wire
// path uses, so the row carries whatever a real task carries.
func (e *Env) logTask(t *testing.T, subject string, assignee ids.UUID, due time.Time) ids.UUID {
	t.Helper()
	assigneeID := ids.From[ids.UserKind](assignee)
	activity, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "task", Subject: &subject, DueAt: &due, AssigneeID: &assigneeID, Source: "manual",
	})
	if err != nil {
		t.Fatalf("logging task %q: %v", subject, err)
	}
	return ids.UUID(activity.Id)
}

func TestThePipelineListAnswersIncludeArchived(t *testing.T) {
	e := Setup(t)
	if _, err := e.Deals.CreatePipeline(e.Admin(), deals.CreatePipelineInput{Name: "Live"}); err != nil {
		t.Fatalf("seeding the live pipeline: %v", err)
	}
	retired, err := e.Deals.CreatePipeline(e.Admin(), deals.CreatePipelineInput{Name: "Retired"})
	if err != nil {
		t.Fatalf("seeding the pipeline to retire: %v", err)
	}
	// Aged by SQL because no contract operation archives a pipeline today.
	// What makes the parameter answerable is the column and the read's filter
	// on it, and this is the state that filter is about.
	e.WsExec(t, `UPDATE pipeline SET archived_at = now() WHERE id = $1`, retired.Id)

	liveOnly, err := e.Deals.ListPipelines(e.Admin(), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("listing live pipelines: %v", err)
	}
	if !listsPipeline(liveOnly, "Live") || listsPipeline(liveOnly, "Retired") {
		t.Fatalf("the live list = %v, want Live present and Retired absent", pipelineNames(liveOnly))
	}

	withArchived, err := e.Deals.ListPipelines(e.Admin(), storekit.IncludeArchived)
	if err != nil {
		t.Fatalf("listing pipelines including the archived: %v", err)
	}
	if !listsPipeline(withArchived, "Retired") {
		t.Fatalf("include_archived = %v, want the archived pipeline among them — a dropped parameter "+
			"answers the live list either way", pipelineNames(withArchived))
	}
}

// listsPipeline reports whether a page carries the pipeline named.
func listsPipeline(page []crmcontracts.Pipeline, name string) bool {
	return slices.Contains(pipelineNames(page), name)
}

// pipelineNames renders a page for a failure message.
func pipelineNames(page []crmcontracts.Pipeline) []string {
	names := make([]string, 0, len(page))
	for _, p := range page {
		names = append(names, p.Name)
	}
	return names
}

func TestThePersonListNarrowsToOneTeamsRows(t *testing.T) {
	e := Setup(t)
	// Rep1 and Rep2 share Team1; Rep3 sits in Team2.
	mine := e.SeedPerson(t, "Owned By Rep1", &e.Rep1)
	teammates := e.SeedPerson(t, "Owned By Rep2", &e.Rep2)
	e.SeedPerson(t, "Owned By Rep3", &e.Rep3)
	e.SeedPerson(t, "Owned By Nobody", nil)

	team := ids.From[ids.TeamKind](e.Team1)
	page, _, err := e.People.ListPeople(e.Admin(), people.ListPeopleInput{OwnerTeamID: &team})
	if err != nil {
		t.Fatalf("listing people by owner team: %v", err)
	}
	got := make([]string, 0, len(page))
	for _, person := range page {
		got = append(got, ids.UUID(person.Id).String())
	}
	slices.Sort(got)
	want := []string{mine.String(), teammates.String()}
	slices.Sort(want)
	// Both of the team's members, and nobody else's rows. The unowned person is
	// deliberately absent: the `team` ROW SCOPE admits unassigned rows, this
	// FILTER names the ones a team owns, and reading the two as one question is
	// how a rep ends up seeing a queue they were not asked to work.
	if !slices.Equal(got, want) {
		t.Fatalf("owner_team_id returned %v, want exactly the two rows Team1's members own (%v)", got, want)
	}
}

func TestThePersonListNarrowsToTheUnownedQueue(t *testing.T) {
	e := Setup(t)
	e.SeedPerson(t, "Owned By Rep1", &e.Rep1)
	unowned := e.SeedPerson(t, "Owned By Nobody", nil)

	yes := true
	page, _, err := e.People.ListPeople(e.Admin(), people.ListPeopleInput{Unassigned: &yes})
	if err != nil {
		t.Fatalf("listing the unowned queue: %v", err)
	}
	if len(page) != 1 || ids.UUID(page[0].Id) != unowned {
		t.Fatalf("unassigned=true returned %d people, want only the unowned one", len(page))
	}

	// `unassigned=false` is not "only owned rows": the reader asked to stop
	// narrowing, and the honest answer is the unnarrowed list.
	no := false
	all, _, err := e.People.ListPeople(e.Admin(), people.ListPeopleInput{Unassigned: &no})
	if err != nil {
		t.Fatalf("listing with unassigned=false: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("unassigned=false returned %d people, want both — it asks for no narrowing at all", len(all))
	}
}

func TestTheOwnerDialsRefuseEachOther(t *testing.T) {
	e := Setup(t)
	e.SeedPerson(t, "Owned By Rep1", &e.Rep1)

	owner := ids.From[ids.UserKind](e.Rep1)
	team := ids.From[ids.TeamKind](e.Team1)
	yes := true
	for _, in := range []people.ListPeopleInput{
		{OwnerID: &owner, Unassigned: &yes},
		{OwnerID: &owner, OwnerTeamID: &team},
		{OwnerTeamID: &team, Unassigned: &yes},
	} {
		// Each pair can only ever match nothing, and an empty page is
		// indistinguishable from an honest one — so the query is refused
		// rather than answered.
		if _, _, err := e.People.ListPeople(e.Admin(), in); err == nil {
			t.Fatal("two owner dials at once were accepted; want a validation refusal")
		}
	}
}

// teamScopedRep is a rep at RowScopeTeam — the tier AAD-ROLE-2 gives Rep and
// Manager. The owner filters are only safe if they narrow WITHIN this; an
// unbounded admin cannot show that, because there is nothing left to narrow.
func teamScopedRep(e *Env, user ids.UUID, teams []ids.UUID) context.Context {
	return e.As(user, teams, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"person":       {Read: true},
			"organization": {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	})
}

func TestTheTeamFilterCannotReachAnotherTeamsRows(t *testing.T) {
	e := Setup(t)
	// Rep1+Rep2 share Team1; Rep3 sits in Team2.
	e.SeedPerson(t, "Owned By Rep1", &e.Rep1)
	outsider := e.SeedPerson(t, "Owned By Rep3", &e.Rep3)

	rep := teamScopedRep(e, e.Rep1, []ids.UUID{e.Team1})
	other := ids.From[ids.TeamKind](e.Team2)
	page, _, err := e.People.ListPeople(rep, people.ListPeopleInput{OwnerTeamID: &other})
	if err != nil {
		t.Fatalf("listing another team's rows: %v", err)
	}
	// Naming a team the caller cannot see filters THEIR OWN visible rows to
	// nothing. It must never reach that team's records: the filter is ANDed
	// onto the row-scope predicate, so it can only ever subtract.
	for _, person := range page {
		if ids.UUID(person.Id) == outsider {
			t.Fatal("owner_team_id reached a row outside the caller's row scope — the filter widened authorization")
		}
	}
	if len(page) != 0 {
		t.Fatalf("owner_team_id=<other team> returned %d rows, want none", len(page))
	}
}

func TestTheOrganizationListNarrowsToOneTeamAndToTheUnownedQueue(t *testing.T) {
	e := Setup(t)
	held := e.SeedOrg(t, "Owned By Rep1", &e.Rep1)
	e.SeedOrg(t, "Owned By Rep3", &e.Rep3)
	unowned := e.SeedOrg(t, "Owned By Nobody", nil)

	team := ids.From[ids.TeamKind](e.Team1)
	page, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{OwnerTeamID: &team})
	if err != nil {
		t.Fatalf("listing organizations by owner team: %v", err)
	}
	if len(page) != 1 || ids.UUID(page[0].Id) != held {
		t.Fatalf("owner_team_id returned %d organizations, want only the one Team1 owns", len(page))
	}

	yes := true
	queue, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{Unassigned: &yes})
	if err != nil {
		t.Fatalf("listing the unowned organizations: %v", err)
	}
	if len(queue) != 1 || ids.UUID(queue[0].Id) != unowned {
		t.Fatalf("unassigned=true returned %d organizations, want only the unowned one", len(queue))
	}
}

func TestThePersonListNarrowsToOneEmployer(t *testing.T) {
	e := Setup(t)
	acme := e.SeedOrg(t, "Acme", nil)
	other := e.SeedOrg(t, "Other", nil)
	staff := e.SeedPerson(t, "Works At Acme", nil)
	leaver := e.SeedPerson(t, "Left Acme", nil)
	elsewhere := e.SeedPerson(t, "Works Elsewhere", nil)

	// Seeded through the real writer, so the edge carries whatever that writer
	// stamps: a hand-inserted row proves nothing about the rows production makes.
	employ := func(person ids.UUID, org ids.UUID, current bool) {
		t.Helper()
		personID := ids.From[ids.PersonKind](person)
		orgID := ids.From[ids.OrganizationKind](org)
		if _, err := e.People.CreateRelationship(e.Admin(), people.CreateRelationshipInput{
			Kind:             "employment",
			PersonID:         &personID,
			OrganizationID:   &orgID,
			IsCurrentPrimary: current,
			Source:           "manual",
		}); err != nil {
			t.Fatalf("seeding the employment edge: %v", err)
		}
	}
	employ(staff, acme, true)
	// A past employer at the same account: the filter answers who works there,
	// not who has ever worked there, and returning the leaver beside the staff
	// is the wrong answer wearing the right shape.
	employ(leaver, acme, false)
	employ(elsewhere, other, true)

	orgID := ids.From[ids.OrganizationKind](acme)
	page, _, err := e.People.ListPeople(e.Admin(), people.ListPeopleInput{OrganizationID: &orgID})
	if err != nil {
		t.Fatalf("listing people by employer: %v", err)
	}
	if len(page) != 1 || ids.UUID(page[0].Id) != staff {
		got := make([]string, 0, len(page))
		for _, person := range page {
			got = append(got, person.FullName)
		}
		t.Fatalf("organization_id returned %v, want only the current employee", got)
	}
}

// setFirmographics writes industry and size through the store the product
// writes them with, so the rows under test are the rows production makes.
func setFirmographics(t *testing.T, e *Env, org ids.UUID, industry, band string) {
	t.Helper()
	if _, err := e.People.UpdateOrganization(e.Admin(), ids.From[ids.OrganizationKind](org),
		people.UpdateOrganizationInput{Industry: &industry, SizeBand: &band}); err != nil {
		t.Fatalf("setting firmographics: %v", err)
	}
}

func TestTheOrganizationListNarrowsByIndustryAndSizeBand(t *testing.T) {
	e := Setup(t)
	small := e.SeedOrg(t, "Small Software", nil)
	big := e.SeedOrg(t, "Big Software", nil)
	e.SeedOrg(t, "Small Logistics", nil)

	setFirmographics(t, e, small, "Software", "1-10")
	setFirmographics(t, e, big, "Software", "1001-5000")

	industry := "Software"
	page, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{Industry: &industry})
	if err != nil {
		t.Fatalf("listing by industry: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("industry=Software returned %d accounts, want both software accounts", len(page))
	}

	band := "1-10"
	sized, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{SizeBand: &band})
	if err != nil {
		t.Fatalf("listing by size band: %v", err)
	}
	if len(sized) != 1 || ids.UUID(sized[0].Id) != small {
		t.Fatalf("size_band=1-10 returned %d accounts, want only the small one", len(sized))
	}

	// An unknown band is refused rather than answered with an empty page:
	// empty reads as "no accounts that size", which is a different claim.
	unknown := "12-13"
	if _, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{SizeBand: &unknown}); err == nil {
		t.Fatal("an unknown size band was accepted; want a validation refusal")
	}
}
