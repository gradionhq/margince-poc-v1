// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The project page is one read that hands back nine sections at once, so it
// is nine chances to out-see the endpoint each section summarizes. What this
// suite pins, over rows the REAL writers produced (CreateProject,
// AdvanceProjectPhase, CreateDeal, SetProjectStakeholder, LogActivity):
//
//   - the phase history carries the birth row and the transition, and the
//     fold over them measures the stay between the two;
//   - the deals, the seats and the timeline come back, the timeline cut at
//     25 with has_more raised;
//   - the filing coverage counts what is on the project and what circles it;
//   - a section the caller may not read is OMITTED and NAMED, never returned
//     empty;
//   - a caller with no sight of the project is refused as the project read
//     itself refuses.

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/project360"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// project360RepPerms is a rep who may read every section: the project and
// its company, deals, people, seats, contracts and activities. Its own
// fixture rather than AccountRepPerms plus a delta, for the reason that file
// gives: a widened shared fixture makes other suites pass while proving
// nothing.
var project360RepPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"project":               {Read: true},
		"organization":          {Read: true},
		"person":                {Read: true},
		"deal":                  {Read: true},
		"activity":              {Read: true},
		"relationship":          {Read: true},
		"contract":              {Read: true},
		"installation_settings": {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

// withoutGrant is the same rep minus one object grant — the fixture that
// proves omission is distinguishable from emptiness.
func withoutGrant(base principal.Permissions, object string) principal.Permissions {
	objects := make(map[string]principal.ObjectGrant, len(base.Objects))
	for k, v := range base.Objects {
		if k != object {
			objects[k] = v
		}
	}
	return principal.Permissions{RoleKeys: base.RoleKeys, Objects: objects, RowScope: base.RowScope}
}

type project360Fixture struct {
	project ids.ProjectID
	deal    ids.UUID
	person  ids.UUID
}

// seedProject360 builds a project with one pursuit, one seat, 26 notes and
// one overdue task filed under it, and three activities circling it: one on
// its deal, one on its stakeholder, and one on the stakeholder that is filed
// under ANOTHER project — the one that must not count as this project's debt.
func seedProject360(t *testing.T, e *Env) project360Fixture {
	t.Helper()
	admin := e.Admin()
	pipeline, open, _ := DealFixture(t, e)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	project := seedProject(admin, t, e, "ERP rollout", strPtr("ERP-27"), org, &e.Rep1).ID
	other := seedProject(admin, t, e, "Datacentre migration", strPtr("DC-4"), org, &e.Rep1).ID
	if _, err := e.Deals.AdvanceProjectPhase(admin, project, deals.AdvanceProjectPhaseInput{ToPhase: "pursuing"}); err != nil {
		t.Fatalf("advance the project: %v", err)
	}

	amount := int64(100_000)
	currency := "EUR"
	orgID := orgIDOf(org)
	deal, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
		Name: "ERP licences", PipelineID: pipeline, StageID: open, OrganizationID: &orgID,
		ProjectID: &project, AmountMinor: &amount, Currency: &currency, OwnerID: userIDPtr(&e.Rep1),
	})
	if err != nil {
		t.Fatalf("create the project's deal: %v", err)
	}
	person := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	if _, err := e.People.SetProjectStakeholder(admin, people.SetProjectStakeholderInput{
		ProjectID: project, PersonID: PersonIDOf(person), Role: "champion",
	}); err != nil {
		t.Fatalf("seat the stakeholder: %v", err)
	}

	log := func(kind, subject string, due *time.Time, links ...activities.ActivityLinkInput) {
		t.Helper()
		if _, _, err := e.Activities.LogActivity(admin, activities.LogActivityInput{
			Kind: kind, Subject: &subject, DueAt: due, Links: links,
		}); err != nil {
			t.Fatalf("log %q: %v", subject, err)
		}
	}
	onProject := activities.ActivityLinkInput{EntityType: "project", EntityID: project.UUID}
	for i := range 26 {
		log("note", fmt.Sprintf("Workshop %d", i), nil, onProject)
	}
	yesterday := time.Now().UTC().Add(-24 * time.Hour)
	log("task", "Send the revised SOW", &yesterday, onProject)
	log("note", "Pricing call", nil, activities.ActivityLinkInput{EntityType: "deal", EntityID: ids.UUID(deal.Id)})
	log("email", "Invoice question", nil, activities.ActivityLinkInput{EntityType: "person", EntityID: person})
	log("email", "Rack decommissioning", nil,
		activities.ActivityLinkInput{EntityType: "person", EntityID: person},
		activities.ActivityLinkInput{EntityType: "project", EntityID: other.UUID})
	return project360Fixture{project: project, deal: ids.UUID(deal.Id), person: person}
}

func project360Service(e *Env, now time.Time) *project360.Service {
	return project360.NewService(e.Pool, e.Deals, e.People, e.Contracts, e.Activities, func() time.Time { return now })
}

func TestProject360AssemblesEverySectionFromTheRealWriters(t *testing.T) {
	e := Setup(t)
	f := seedProject360(t, e)
	now := time.Now().UTC()
	page, err := project360Service(e, now).Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, project360RepPerms), f.project)
	if err != nil {
		t.Fatalf("assemble as a fully-granted rep: %v", err)
	}
	if len(page.SectionsOmitted) != 0 {
		t.Errorf("sections_omitted = %v, want none for a rep holding every grant", page.SectionsOmitted)
	}
	if page.AsOf != now {
		t.Errorf("as_of = %v, want the pinned instant %v", page.AsOf, now)
	}
	assertProject360History(t, page, now)
	assertProject360Collections(t, page, f)
	assertProject360Counts(t, page)
}

// The history is the two rows the writers appended, and the fold measures
// the stay between them from the rows' own instants.
func assertProject360History(t *testing.T, page crmcontracts.Project360, now time.Time) {
	t.Helper()
	if page.PhaseHistory == nil {
		t.Fatal("phase_history absent for a rep holding the project grant")
	}
	rows := page.PhaseHistory.Data
	if len(rows) != 2 || rows[0].FromPhase != nil || rows[0].ToPhase != deals.PhaseInitiative ||
		rows[1].FromPhase == nil || *rows[1].FromPhase != deals.PhaseInitiative || rows[1].ToPhase != "pursuing" {
		t.Fatalf("phase_history.data = %+v, want the birth row then initiative→pursuing", rows)
	}
	if rows[1].ChangedBy.DisplayName == nil || *rows[1].ChangedBy.DisplayName == "" {
		t.Errorf("the transition names no display name for changed_by %q; the admin seat is a real app user", rows[1].ChangedBy.Id)
	}
	durations := page.PhaseHistory.PhaseDurations
	wantInitiative := int64(rows[1].ChangedAt.Sub(rows[0].ChangedAt) / time.Second)
	wantPursuing := int64(now.Sub(rows[1].ChangedAt) / time.Second)
	if len(durations) != 2 ||
		durations[0].Phase != deals.PhaseInitiative || durations[0].Seconds != wantInitiative || durations[0].Current ||
		durations[1].Phase != "pursuing" || durations[1].Seconds != wantPursuing || !durations[1].Current {
		t.Errorf("phase_durations = %+v, want initiative=%ds then pursuing=%ds (current)", durations, wantInitiative, wantPursuing)
	}
}

// The deals, the seats and the timeline come back as the writers left them,
// and the timeline reports that it was cut.
func assertProject360Collections(t *testing.T, page crmcontracts.Project360, f project360Fixture) {
	t.Helper()
	if page.Organization == nil || page.Organization.Name != "Acme" {
		t.Errorf("organization = %+v, want Acme", page.Organization)
	}
	if page.Deals == nil || len(page.Deals.Data) != 1 || ids.UUID(page.Deals.Data[0].Id) != f.deal || page.Deals.Page.HasMore {
		t.Errorf("deals = %+v, want exactly the project's one deal", page.Deals)
	}
	if page.Stakeholders == nil || len(page.Stakeholders.Data) != 1 {
		t.Fatalf("stakeholders = %+v, want the one seat", page.Stakeholders)
	}
	seat := page.Stakeholders.Data[0]
	if ids.UUID(seat.PersonId) != f.person || seat.PersonName == nil || *seat.PersonName != "Dana Buyer" ||
		seat.Role == nil || *seat.Role != "champion" {
		t.Errorf("seat = %+v, want Dana Buyer as champion", seat)
	}
	if page.Activities == nil || len(page.Activities.Data) != 25 || !page.Activities.Page.HasMore {
		t.Errorf("activities: %d rows, has_more=%v — want 25 rows and has_more after 27 filed activities",
			len(page.Activities.Data), page.Activities != nil && page.Activities.Page.HasMore)
	}
	if page.Commitments == nil || len(page.Commitments.Data) != 1 || !page.Commitments.Data[0].Overdue {
		t.Errorf("commitments = %+v, want the one overdue task", page.Commitments)
	}
	if page.Contracts == nil || len(page.Contracts.Data) != 0 {
		t.Errorf("contracts = %+v, want an EMPTY section for a granted rep — empty and withheld are different facts", page.Contracts)
	}
	if page.Documents == nil || len(page.Documents.Data) != 0 {
		t.Errorf("documents = %+v, want an empty section", page.Documents)
	}
}

// 27 activities are filed under the project; two circle it unfiled (one on
// its deal, one on its stakeholder); the one on the stakeholder that is filed
// under the OTHER project is somebody's and does not count.
func assertProject360Counts(t *testing.T, page crmcontracts.Project360) {
	t.Helper()
	if page.Coverage == nil || page.Coverage.Attributed != 27 || page.Coverage.UnattributedNearby != 2 {
		t.Errorf("coverage = %+v, want attributed=27 unattributed_nearby=2", page.Coverage)
	}
	if page.Rollups == nil {
		t.Fatal("rollups absent for a rep holding both the deal and the activity grant")
	}
	r := page.Rollups
	if r.OpenDealValue.AmountMinor == nil || *r.OpenDealValue.AmountMinor != 100_000 ||
		r.WonDealValue.AmountMinor == nil || *r.WonDealValue.AmountMinor != 0 {
		t.Errorf("rollups money = open %v won %v, want open 100000 and won 0", r.OpenDealValue, r.WonDealValue)
	}
	if r.OpenCommitments != 1 || r.ActivityCount != 27 || r.LastActivityAt == nil {
		t.Errorf("rollups = %+v, want open_commitments=1 activity_count=27 and a last_activity_at", r)
	}
}

func TestProject360OmitsAndNamesTheSectionTheCallerMayNotRead(t *testing.T) {
	e := Setup(t)
	f := seedProject360(t, e)
	svc := project360Service(e, time.Now().UTC())
	page, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, withoutGrant(project360RepPerms, "contract")), f.project)
	if err != nil {
		t.Fatalf("assemble as a rep without the contract grant: %v", err)
	}
	if page.Contracts != nil {
		t.Error("contracts section present for a rep who cannot read contracts — an omitted section must be absent, not empty")
	}
	if !slices.Contains(page.SectionsOmitted, crmcontracts.Project360SectionContracts) {
		t.Errorf("sections_omitted = %v, want it to name contracts", page.SectionsOmitted)
	}
	// Losing one grant narrows the page; it does not refuse it.
	if page.Project.Name != "ERP rollout" || page.Deals == nil || page.Activities == nil {
		t.Errorf("the rest of the page must still be served: project=%q deals=%v activities=%v",
			page.Project.Name, page.Deals != nil, page.Activities != nil)
	}
}

// A project is readable by every seat holding the project grant, so "no
// sight" has three spellings here: no grant (refused), an archived project
// (not found, as the live-only read answers), and an id that names nothing.
func TestProject360RefusesACallerWithNoSightOfTheProject(t *testing.T) {
	e := Setup(t)
	f := seedProject360(t, e)
	svc := project360Service(e, time.Now().UTC())
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, project360RepPerms)

	_, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, withoutGrant(project360RepPerms, "project")), f.project)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("assemble without the project grant → %v, want ErrPermissionDenied", err)
	}
	_, err = svc.Assemble(ctx, ids.From[ids.ProjectKind](ids.NewV7()))
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("assemble on an id that names nothing → %v, want ErrNotFound", err)
	}
	if _, err := e.Deals.ArchiveProject(e.Admin(), f.project, nil); err != nil {
		t.Fatalf("archive the project: %v", err)
	}
	_, err = svc.Assemble(ctx, f.project)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("assemble on an archived project → %v, want ErrNotFound (the live-only anchor read)", err)
	}
	// The positive control: the same call served the page a moment ago.
	if _, err := project360Service(e, time.Now().UTC()).Assemble(ctx, seedProject(e.Admin(), t, e, "Fresh", nil, e.SeedOrg(t, "Beta", &e.Rep1), nil).ID); err != nil {
		t.Errorf("assemble on a live project the caller may read: %v", err)
	}
}
