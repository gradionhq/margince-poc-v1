// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Projects on the relationship surfaces, and a draft attributed to one at
// composition. The two record pages list the bodies of work a company
// carries and a person is part of; a draft that names one is grounded in the
// page SCOPED to it, so the other project's correspondence never reaches the
// model; and the context walk an agent catches up from reports the projects
// an account's mail is filed under.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/accountdraft"
	"github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/compose/person360"
	"github.com/gradionhq/margince/backend/internal/compose/persondraft"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

func orgSurfaceService(e *Env) *org360.Service {
	return org360.NewService(e.Pool, e.People, e.Deals, approvals.NewService(e.DB()),
		func() time.Time { return roomFixedNow })
}

// projectKeys indexes a page's projects section by key, so a test reads
// "ERP-27" rather than an id it would have to thread through the fixture.
func projectKeys(projects *[]crmcontracts.Organization360Project) map[string]crmcontracts.Organization360Project {
	out := map[string]crmcontracts.Organization360Project{}
	if projects == nil {
		return out
	}
	for _, p := range *projects {
		if p.Key != nil {
			out[*p.Key] = p
		}
	}
	return out
}

// employAtAccount makes the fixture's contact a current employee of the
// fixture's company, which is what puts them on the company page's people
// section — the drafter's recipient lookup — and gives the person page an
// employer whose projects are theirs.
func employAtAccount(t *testing.T, e *Env, f scopeFixture) {
	t.Helper()
	personID, orgID := PersonIDOf(f.person), orgIDOf(f.org)
	primary := true
	if _, err := e.People.CreateRelationship(e.Admin(), people.CreateRelationshipInput{
		Kind: "employment", PersonID: &personID, OrganizationID: &orgID, IsCurrentPrimary: &primary,
	}); err != nil {
		t.Fatalf("employing the contact: %v", err)
	}
}

func TestOrganization360ListsTheAccountsLiveProjectsWorkInMotionFirst(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	if _, err := e.Deals.AdvanceProjectPhase(e.Admin(), f.erp, deals.AdvanceProjectPhaseInput{ToPhase: "pursuing"}); err != nil {
		t.Fatalf("advancing the ERP project: %v", err)
	}

	page, err := orgSurfaceService(e).Assemble(e.Admin(), orgIDOf(f.org))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if page.Projects == nil {
		t.Fatalf("the projects section was withheld from an unbounded caller; sections_omitted = %v", page.SectionsOmitted)
	}
	if len(*page.Projects) != 2 {
		t.Fatalf("projects = %d, want the account's two", len(*page.Projects))
	}
	// The pursuing project is in motion and leads; the initiative follows.
	if got := *(*page.Projects)[0].Key; got != "ERP-27" {
		t.Errorf("first project = %q, want the pursuing ERP-27 ahead of the initiative", got)
	}
	byKey := projectKeys(page.Projects)
	if byKey["ERP-27"].Phase != crmcontracts.Organization360ProjectPhasePursuing {
		t.Errorf("ERP-27 phase = %q, want pursuing", byKey["ERP-27"].Phase)
	}
	if byKey["DC-4"].Name != "Datacentre migration" {
		t.Errorf("DC-4 name = %q, want the project's name", byKey["DC-4"].Name)
	}
	if byKey["ERP-27"].LastActivityAt == nil {
		t.Error("ERP-27 carries no last_activity_at though mail is filed under it")
	}
}

// Without the project grant the section is NAMED, not returned empty: a
// company with no projects and a caller who may not see them are different
// facts, and the page must not render them the same way.
func TestBothPagesNameTheProjectsSectionWhenTheCallerLacksTheGrant(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	employAtAccount(t, e, f)
	noProjectGrant := e.As(e.Rep1, []ids.UUID{e.Team1}, withoutGrant(roomPerms, "project"))

	orgPage, err := orgSurfaceService(e).Assemble(noProjectGrant, orgIDOf(f.org))
	if err != nil {
		t.Fatalf("assemble company page: %v", err)
	}
	if orgPage.Projects != nil {
		t.Error("the company page served projects to a caller with no project grant")
	}
	if !containsOrgSection(orgPage.SectionsOmitted, crmcontracts.Organization360SectionsOmittedProjects) {
		t.Errorf("company page sections_omitted = %v, want projects named", orgPage.SectionsOmitted)
	}

	personPage, err := personRoomService(e).Assemble(noProjectGrant, PersonIDOf(f.person))
	if err != nil {
		t.Fatalf("assemble person page: %v", err)
	}
	if personPage.Projects != nil {
		t.Error("the person page served projects to a caller with no project grant")
	}
	if !containsPersonSection(personPage.SectionsOmitted, crmcontracts.Person360SectionsOmittedProjects) {
		t.Errorf("person page sections_omitted = %v, want projects named", personPage.SectionsOmitted)
	}
}

func containsOrgSection(names []crmcontracts.Organization360SectionsOmitted, want crmcontracts.Organization360SectionsOmitted) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func containsPersonSection(names []crmcontracts.Person360SectionsOmitted, want crmcontracts.Person360SectionsOmitted) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// A person is part of a project through a seat on it, or through the company
// they work for today. The two routes are proved separately: a seat alone
// names one project, and employment then brings the employer's other one —
// without a second row for the project both routes reach.
func TestPerson360ListsTheProjectsAPersonIsPartOf(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	svc := personRoomService(e)
	personID := PersonIDOf(f.person)

	before, err := svc.Assemble(e.Admin(), personID)
	if err != nil {
		t.Fatalf("assemble before any tie: %v", err)
	}
	if before.Projects == nil || len(*before.Projects) != 0 {
		t.Fatalf("a person with no seat and no employer lists projects = %v, want an empty section", before.Projects)
	}

	if _, err := e.People.SetProjectStakeholder(e.Admin(), people.SetProjectStakeholderInput{
		ProjectID: f.erp, PersonID: personID, Role: "sponsor",
	}); err != nil {
		t.Fatalf("seating the contact on the ERP project: %v", err)
	}
	seated, err := svc.Assemble(e.Admin(), personID)
	if err != nil {
		t.Fatalf("assemble with a seat: %v", err)
	}
	if keys := projectKeys(seated.Projects); len(keys) != 1 || keys["ERP-27"].Name == "" {
		t.Fatalf("a seat on ERP-27 lists %v, want exactly that project", seated.Projects)
	}

	employAtAccount(t, e, f)
	employed, err := svc.Assemble(e.Admin(), personID)
	if err != nil {
		t.Fatalf("assemble with an employer: %v", err)
	}
	if len(*employed.Projects) != 2 {
		t.Fatalf("seat + employer lists %d projects, want 2 — one row per project, no duplicate for the one both routes reach", len(*employed.Projects))
	}
	keys := projectKeys(employed.Projects)
	if keys["DC-4"].Name == "" {
		t.Error("the employer's other project is missing from the person page")
	}
}

// The account drafter scoped to one project reads the page scoped to it:
// the project's own facts are folded in, the unfiled mail stays, and the
// other engagement's correspondence is not there to be drawn on. The
// unscoped fold of the same account still carries it, so the narrowing is
// the scope's doing.
func TestAccountDraftScopedToAProjectGroundsOnItAndNotTheOther(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	employAtAccount(t, e, f)
	svc := orgSurfaceService(e)
	req := accountdraft.Request{PersonID: f.person.String(), ProjectID: &f.erp}

	scoped, err := svc.AssembleScoped(e.Admin(), orgIDOf(f.org), org360.AssembleOptions{ProjectID: &f.erp})
	if err != nil {
		t.Fatalf("assemble scoped: %v", err)
	}
	in, err := accountdraft.FromView(scoped, req)
	if err != nil {
		t.Fatalf("fold the scoped view: %v", err)
	}
	recent := recentAccountIDs(in.Recent)
	if recent[f.onOther] {
		t.Error("the other engagement's mail reached the draft's grounding — the draft did not read the scoped page")
	}
	if !recent[f.onERP] || !recent[f.unfiled] {
		t.Errorf("the scoped project's own mail or the unfiled mail is missing from the grounding: %v", recent)
	}
	if in.Project == nil {
		t.Fatal("the draft carries no project fact though the request named one")
	}
	if in.Project.Key != "ERP-27" || in.Project.Name != "ERP rollout" || in.Project.Phase != "initiative" {
		t.Errorf("project fact = %+v, want ERP-27 / ERP rollout / initiative", in.Project)
	}
	if in.Project.OpenCommitments != 1 {
		t.Errorf("open commitments = %d, want the ERP task alone — the other engagement's task is out of scope", in.Project.OpenCommitments)
	}

	wide, err := svc.Assemble(e.Admin(), orgIDOf(f.org))
	if err != nil {
		t.Fatalf("assemble unscoped: %v", err)
	}
	unscoped, err := accountdraft.FromView(wide, accountdraft.Request{PersonID: f.person.String()})
	if err != nil {
		t.Fatalf("fold the unscoped view: %v", err)
	}
	if !recentAccountIDs(unscoped.Recent)[f.onOther] {
		t.Error("an unscoped fold lost the other engagement's mail, so the scoped absence proves nothing")
	}

	// Through the service, the same request is answered (deterministic
	// floor, no model lane) and a project of another company is refused as
	// a field error rather than grounding this account's draft in it.
	draft := accountdraft.NewService(svc, nil)
	if _, err := draft.Draft(e.Admin(), orgIDOf(f.org), req); err != nil {
		t.Fatalf("draft scoped to the account's own project: %v", err)
	}
	elsewhere := e.SeedOrg(t, "Other GmbH", &e.Rep1)
	foreign, err := e.Deals.CreateProject(e.Admin(), deals.CreateProjectInput{
		Name: "Foreign work", OrganizationID: orgIDOf(elsewhere), Source: "manual",
	})
	if err != nil {
		t.Fatalf("create the other company's project: %v", err)
	}
	foreignID := projectIDOf(ids.UUID(foreign.Id))
	_, err = draft.Draft(e.Admin(), orgIDOf(f.org), accountdraft.Request{PersonID: f.person.String(), ProjectID: &foreignID})
	var detailed *httperr.DetailedError
	if !errors.As(err, &detailed) || detailed.Status != 422 {
		t.Errorf("draft scoped to another company's project: err = %v, want a 422 naming project_id", err)
	}
}

func recentAccountIDs(recent []accountdraft.ActIn) map[string]bool {
	out := map[string]bool{}
	for _, act := range recent {
		out[act.ID] = true
	}
	return out
}

// The person drafter's mirror of the account case.
func TestPersonDraftScopedToAProjectGroundsOnItAndNotTheOther(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	employAtAccount(t, e, f)
	svc := personRoomService(e)
	personID := PersonIDOf(f.person)

	scoped, err := svc.AssembleScoped(e.Admin(), personID, person360.AssembleOptions{ProjectID: &f.erp})
	if err != nil {
		t.Fatalf("assemble scoped: %v", err)
	}
	in := persondraft.FromView(scoped, persondraft.Request{ProjectID: &f.erp})
	recent := map[string]bool{}
	for _, act := range in.Recent {
		recent[act.ID] = true
	}
	if recent[f.onOther] {
		t.Error("the other engagement's mail reached the draft's grounding — the draft did not read the scoped page")
	}
	if !recent[f.onERP] || !recent[f.unfiled] {
		t.Errorf("the scoped project's own mail or the unfiled mail is missing from the grounding: %v", recent)
	}
	if in.Project == nil || in.Project.Key != "ERP-27" {
		t.Fatalf("project fact = %+v, want ERP-27 folded in", in.Project)
	}

	wide, err := svc.Assemble(e.Admin(), personID)
	if err != nil {
		t.Fatalf("assemble unscoped: %v", err)
	}
	unscopedRecent := map[string]bool{}
	for _, act := range persondraft.FromView(wide, persondraft.Request{}).Recent {
		unscopedRecent[act.ID] = true
	}
	if !unscopedRecent[f.onOther] {
		t.Error("an unscoped fold lost the other engagement's mail, so the scoped absence proves nothing")
	}

	if _, err := persondraft.NewService(svc, nil).Draft(e.Admin(), personID, persondraft.Request{ProjectID: &f.erp}); err != nil {
		t.Fatalf("draft scoped to a project the person is part of: %v", err)
	}
}

// The context walk around the account reports the projects its mail is filed
// under, so an agent catching up on the company learns which bodies of work
// the correspondence belongs to.
func TestAssembledContextOnTheAccountReportsRelatedProjects(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	retriever := search.NewRetriever(search.NewStore(harnessDB(e.Pool, e.WS)), nil)
	anchor := datasource.EntityRef{Type: datasource.EntityOrganization, ID: f.org}

	got, err := retriever.AssembleContext(e.Admin(), anchor, retrieval.AssembleOptions{MaxItems: 25})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	var related []retrieval.Item
	for _, section := range got.Sections {
		if section.Name == "related_projects" {
			related = section.Items
		}
	}
	if len(related) != 2 {
		t.Fatalf("related_projects = %+v, want the two projects the account's mail is filed under", related)
	}
	seen := map[string]bool{}
	for _, item := range related {
		seen[item.Ref.ID.String()] = true
	}
	if !seen[f.erp.String()] {
		t.Error("the ERP project is missing from related_projects")
	}
}

// The composer's send path files a message under a person AND a project in
// one create, and the retention evidence that makes it business
// correspondence lands in the same transaction.
func TestLoggingAnActivityWithAPersonAndAProjectLinkWritesBothAndTheEvidence(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	subject := "Cutover date confirmed"
	logged, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "email", Direction: strPtr("outbound"), Subject: &subject,
		Links: []activities.ActivityLinkInput{
			{EntityType: "person", EntityID: f.person},
			{EntityType: "project", EntityID: f.erp.UUID},
		},
	})
	if err != nil {
		t.Fatalf("log with a person and a project link: %v", err)
	}
	activityID := ids.UUID(logged.Id)

	personLinks := countLinks(t, e, activityID, "person_id", f.person)
	projectLinks := countLinks(t, e, activityID, "project_id", f.erp.UUID)
	if personLinks != 1 || projectLinks != 1 {
		t.Errorf("links: person = %d, project = %d, want one of each", personLinks, projectLinks)
	}
	stamp := readProjectStamp(t, e, activityID)
	if stamp.evidence != 1 || stamp.class == nil || *stamp.class != "commercial_correspondence" {
		t.Errorf("evidence rows = %d, class = %v; want one project_linked row and commercial_correspondence", stamp.evidence, stamp.class)
	}
}

// countLinks counts the activity's links on one target column; the column is
// a compile-time literal and the placeholders are derived from the args.
func countLinks(t *testing.T, e *Env, activityID ids.UUID, column string, target ids.UUID) int {
	t.Helper()
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	query := fmt.Sprintf(`SELECT count(*) FROM activity_link WHERE activity_id = $%d AND %s = $%d`,
		arg(activityID), column, arg(target))
	return e.WsCount(t, query, args...)
}

// The employment route is an EDGE read, bounded like the seat route: a rep
// whose relationship scope excludes the employer's company may not learn that
// company's projects through the person who works there. The admit case runs
// first, so the refusal below cannot pass against a read that admits nobody.
func TestPerson360WithholdsTheEmployersProjectWhenTheEmploymentEdgeIsOutOfScope(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	employAtAccount(t, e, f)
	svc := personRoomService(e)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)

	admitted, err := svc.Assemble(rep, PersonIDOf(f.person))
	if err != nil {
		t.Fatalf("assemble with the employer in scope: %v", err)
	}
	if admitted.Projects == nil || len(*admitted.Projects) != 2 {
		t.Fatalf("a rep with the employer in scope lists %v, want both projects — the refusal below would prove nothing", admitted.Projects)
	}

	e.MakeCapturePrivate(t, "organization", f.org, e.Rep3)
	withheld, err := svc.Assemble(rep, PersonIDOf(f.person))
	if err != nil {
		t.Fatalf("assemble with the employer out of scope: %v", err)
	}
	if withheld.Projects == nil {
		t.Fatalf("the projects section was withheld outright; sections_omitted = %v", withheld.SectionsOmitted)
	}
	if len(*withheld.Projects) != 0 {
		t.Errorf("the employer's projects reached a rep whose relationship scope excludes the employment edge: %v", *withheld.Projects)
	}
}
