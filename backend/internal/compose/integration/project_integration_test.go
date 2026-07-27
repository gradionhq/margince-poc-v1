// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The project record over real rows: the key rules the schema enforces,
// the phase transition that must write its history in the same
// transaction, the cross-company deal pointer the constraint trigger
// refuses, the archive that ends a grouping without ending what it
// grouped — and the row-scope case that would otherwise fail silently.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedProject creates a project on a fresh company, owned by the given
// user (nil = ownerless).
type projectFixture struct {
	ID      ids.ProjectID
	Version int64
}

func seedProject(ctx context.Context, t *testing.T, e *Env, name string, key *string, org ids.UUID, owner *ids.UUID) projectFixture {
	t.Helper()
	in := deals.CreateProjectInput{
		Name:           name,
		Key:            key,
		OrganizationID: orgIDOf(org),
		OwnerID:        userIDPtr(owner),
		Source:         "manual",
	}
	p, err := e.Deals.CreateProject(ctx, in)
	if err != nil {
		t.Fatalf("create project %q: %v", name, err)
	}
	return projectFixture{ID: projectIDOf(ids.UUID(p.Id)), Version: *p.Version}
}

// A project is born at the head of the ladder with its history already
// complete: the creation row is what makes "how did it get here"
// answerable from the very first read.
func TestProjectIsBornWithItsHistoryRow(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	p := seedProject(e.Admin(), t, e, "ERP replacement", strPtr("ERP-27"), org, nil)

	got, err := e.Deals.GetProject(e.Admin(), p.ID, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase == nil || string(*got.Phase) != deals.PhaseInitiative {
		t.Errorf("phase = %v, want %s", got.Phase, deals.PhaseInitiative)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM project_phase_history WHERE project_id = $1 AND from_phase IS NULL AND to_phase = 'initiative'`,
		p.ID); n != 1 {
		t.Errorf("creation history rows = %d, want exactly 1", n)
	}
}

// The key is unique among LIVE projects and matched case-insensitively —
// and the conflict carries the id of the project already holding it, so a
// caller that collided can open that project rather than hunt for it.
func TestProjectKeyIsUniqueAmongLiveProjectsAndFreedByArchiving(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	first := seedProject(e.Admin(), t, e, "ERP replacement", strPtr("ERP-27"), org, nil)

	_, err := e.Deals.CreateProject(e.Admin(), deals.CreateProjectInput{
		Name: "Second", Key: strPtr("erp-27"), OrganizationID: orgIDOf(org), Source: "manual",
	})
	var taken *deals.ProjectKeyTakenError
	if !errors.As(err, &taken) {
		t.Fatalf("a case-different duplicate key produced %v, want ProjectKeyTakenError", err)
	}
	if taken.ExistingID == nil || *taken.ExistingID != first.ID.UUID {
		t.Errorf("conflict named %v, want the live project %v", taken.ExistingID, first.ID.UUID)
	}

	// Archiving frees the key: the uniqueness index is partial on live rows.
	if _, err := e.Deals.ArchiveProject(e.Admin(), first.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Deals.CreateProject(e.Admin(), deals.CreateProjectInput{
		Name: "Reused", Key: strPtr("ERP-27"), OrganizationID: orgIDOf(org), Source: "manual",
	}); err != nil {
		t.Fatalf("archiving did not free the key: %v", err)
	}
}

// A phase move writes the row change and its history row from ONE
// transaction, and announces itself as a phase change rather than a
// generic update.
func TestAdvanceProjectPhaseWritesHistoryAndTheFirstClassEvent(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	p := seedProject(e.Admin(), t, e, "ERP replacement", nil, org, nil)

	moved, err := e.Deals.AdvanceProjectPhase(e.Admin(), p.ID, deals.AdvanceProjectPhaseInput{ToPhase: "delivering"})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Phase == nil || string(*moved.Phase) != "delivering" {
		t.Errorf("phase = %v, want delivering", moved.Phase)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM project_phase_history WHERE project_id = $1 AND from_phase = 'initiative' AND to_phase = 'delivering'`,
		p.ID); n != 1 {
		t.Errorf("transition history rows = %d, want exactly 1", n)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM event_outbox
		  WHERE envelope->>'type' = 'project.phase_changed' AND envelope->'entity'->>'id' = $1::text`,
		p.ID); n != 1 {
		t.Errorf("project.phase_changed events = %d, want exactly 1", n)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM event_outbox
		  WHERE envelope->>'type' = 'project.updated' AND envelope->'entity'->>'id' = $1::text`,
		p.ID); n != 0 {
		t.Errorf("project.updated events = %d, want 0 — a phase move is not a diff", n)
	}
}

// Closing is a claim that the work ended; an unexplained claim is not
// answerable later, so it is refused.
func TestClosingAProjectRequiresAReason(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	p := seedProject(e.Admin(), t, e, "ERP replacement", nil, org, nil)

	_, err := e.Deals.AdvanceProjectPhase(e.Admin(), p.ID, deals.AdvanceProjectPhaseInput{ToPhase: deals.PhaseClosed})
	var needsReason *deals.ClosedReasonRequiredError
	if !errors.As(err, &needsReason) {
		t.Fatalf("closing without a reason produced %v, want ClosedReasonRequiredError", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM project_phase_history WHERE project_id = $1`, p.ID); n != 1 {
		t.Errorf("history rows = %d, want only the creation row — a refused move records nothing", n)
	}

	// Re-opening clears the closed reason: a live project must not carry
	// the explanation of a close that no longer applies.
	if _, err := e.Deals.AdvanceProjectPhase(e.Admin(), p.ID, deals.AdvanceProjectPhaseInput{
		ToPhase: deals.PhaseClosed, Reason: strPtr("Delivered."),
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := e.Deals.AdvanceProjectPhase(e.Admin(), p.ID, deals.AdvanceProjectPhaseInput{ToPhase: "delivering"})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ClosedReason != nil {
		t.Errorf("closed_reason = %v after re-opening, want nil", *reopened.ClosedReason)
	}
}

// A deal and the project it belongs to must name the same company. The
// rule spans two rows, so it lives in a constraint trigger — and it must
// surface as a named 422, never as an opaque server fault.
func TestADealCannotPointAtAnotherCompanysProject(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	orgA := e.SeedOrg(t, "BAER Pharma", nil)
	orgB := e.SeedOrg(t, "Kessler GmbH", nil)
	p := seedProject(e.Admin(), t, e, "ERP replacement", nil, orgA, nil)

	orgBID := orgIDOf(orgB)
	_, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "Wrong company", PipelineID: pipeline, StageID: open,
		OrganizationID: &orgBID, ProjectID: &p.ID, Source: "manual",
	})
	var mismatch *deals.DealProjectOrgMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("a cross-company pointer produced %v, want DealProjectOrgMismatchError", err)
	}

	orgAID := orgIDOf(orgA)
	if _, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "Right company", PipelineID: pipeline, StageID: open,
		OrganizationID: &orgAID, ProjectID: &p.ID, Source: "manual",
	}); err != nil {
		t.Fatalf("a same-company pointer was refused: %v", err)
	}
}

// Archiving a project ends the grouping, not what it grouped: the
// conversations and the deals survive, and so does the phase history that
// explains where the work got to.
func TestArchivingAProjectKeepsWhatItGrouped(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	p := seedProject(e.Admin(), t, e, "ERP replacement", strPtr("ERP-27"), org, nil)

	orgID := orgIDOf(org)
	d, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "Phase one", PipelineID: pipeline, StageID: open,
		OrganizationID: &orgID, ProjectID: &p.ID, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	act, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "email", Subject: strPtr("[ERP-27] kickoff"), Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "project", EntityID: p.ID.UUID}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.Deals.ArchiveProject(e.Admin(), p.ID, nil); err != nil {
		t.Fatal(err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM deal WHERE id = $1 AND archived_at IS NULL`, ids.UUID(d.Id)); n != 1 {
		t.Error("archiving the project archived its deal — the grouping dies, the deal does not")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM activity WHERE id = $1 AND archived_at IS NULL`, ids.UUID(act.Id)); n != 1 {
		t.Error("archiving the project archived its conversation")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM project_phase_history WHERE project_id = $1`, p.ID); n == 0 {
		t.Error("archiving the project erased its phase history — the history is what survives")
	}
}

// The row-scope case that fails SILENTLY if the activity link-walk has no
// project branch: an activity linked ONLY to a project must follow that
// project's visibility, in both directions. Getting this half-right looks
// exactly like missing data.
func TestAnActivityLinkedOnlyToAProjectFollowsItsRowScope(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	// Real seeded users: owner_id is a composite FK to app_user, so a
	// synthetic uuid would be refused before the scope rule is exercised.
	ownerA := e.Rep1
	ownerB := e.Rep3
	p := seedProject(e.Admin(), t, e, "ERP replacement", nil, org, &ownerA)

	act, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "email", Subject: strPtr("rollout schedule"), Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "project", EntityID: p.ID.UUID}},
	})
	if err != nil {
		t.Fatal(err)
	}

	scoped := principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"project":  {Read: true},
			"activity": {Read: true},
		},
		RowScope: principal.RowScopeOwn,
	}

	// The project's owner sees the conversation about their project.
	if _, err := e.Activities.GetActivity(e.As(ownerA, nil, scoped), ids.From[ids.ActivityKind](ids.UUID(act.Id)), storekit.LiveOnly); err != nil {
		t.Errorf("the project's owner cannot see its activity: %v", err)
	}
	// Someone with no claim on the project does not — and reads as absent,
	// not as forbidden, so existence is not disclosed.
	_, err = e.Activities.GetActivity(e.As(ownerB, nil, scoped), ids.From[ids.ActivityKind](ids.UUID(act.Id)), storekit.LiveOnly)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("a stranger to the project got %v, want ErrNotFound", err)
	}
}

// PROJ-LIFE-4: a project's anchor is NOT NULL … ON DELETE RESTRICT, so it
// cannot stay behind on a dissolved company. Leaving it is not cosmetic —
// the deals move to the survivor and the same-company trigger then refuses
// their NEXT edit, which is how a healthy deal becomes un-editable over a
// mismatch nobody made.
func TestMergingCompaniesReAnchorsTheProjectWithItsDeals(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	source := e.SeedOrg(t, "BAER Pharma GmbH", nil)
	target := e.SeedOrg(t, "BAER Pharma", nil)
	p := seedProject(e.Admin(), t, e, "ERP replacement", nil, source, nil)

	sourceID := orgIDOf(source)
	d, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "Phase one", PipelineID: pipeline, StageID: open,
		OrganizationID: &sourceID, ProjectID: &p.ID, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.People.MergeOrganization(e.Admin(), sourceID, orgIDOf(target)); err != nil {
		t.Fatalf("merge: %v", err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM project WHERE id = $1 AND organization_id = $2`,
		p.ID, target); n != 1 {
		t.Error("the project stayed on the merged-away company")
	}
	// The proof that the re-anchor is load-bearing: editing the deal after
	// the merge must still work. Before the fix this raised the
	// same-company trigger.
	name := "Phase one, renamed"
	if _, err := e.Deals.UpdateDeal(e.Admin(), ids.From[ids.DealKind](ids.UUID(d.Id)),
		deals.UpdateDealInput{Name: &name}); err != nil {
		t.Errorf("the merged deal became un-editable: %v", err)
	}
}

// PROJ-LIFE-4's ask: two companies that each hold live bodies of work may,
// once merged, be running the same one twice or two different ones — and
// nothing in the data says which. The merge stops and names them rather
// than leaving a human to find the duplicates later.
func TestMergingTwoCompaniesThatBothCarryProjectsIsRefused(t *testing.T) {
	e := Setup(t)
	source := e.SeedOrg(t, "BAER Pharma GmbH", nil)
	target := e.SeedOrg(t, "BAER Pharma", nil)
	seedProject(e.Admin(), t, e, "ERP replacement", nil, source, nil)
	kept := seedProject(e.Admin(), t, e, "Validation", nil, target, nil)

	_, err := e.People.MergeOrganization(e.Admin(), orgIDOf(source), orgIDOf(target))
	var both *people.BothCompaniesCarryProjectsError
	if !errors.As(err, &both) {
		t.Fatalf("merging two project-carrying companies produced %v, want a refusal", err)
	}
	if len(both.Source) != 1 || len(both.Target) != 1 {
		t.Errorf("the refusal named %v and %v, want one project from each side", both.Source, both.Target)
	}

	// Refusing must change nothing: the transaction rolls back whole.
	if n := e.WsCount(t, `SELECT count(*) FROM organization WHERE id = $1 AND archived_at IS NULL`, source); n != 1 {
		t.Error("the refused merge still archived the source company")
	}

	// And it is actionable: archive one side, then the merge proceeds.
	if _, err := e.Deals.ArchiveProject(e.Admin(), kept.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.People.MergeOrganization(e.Admin(), orgIDOf(source), orgIDOf(target)); err != nil {
		t.Errorf("archiving one side did not unblock the merge: %v", err)
	}
}

// A119 Amendment 1.A: a project is born in `initiative`, before any deal
// exists, and the object carrying interest at that stage is the lead.
func TestALeadCanBelongToAProject(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	p := seedProject(e.Admin(), t, e, "ERP replacement", nil, org, nil)

	lead, _, err := e.People.CreateLead(e.Admin(), people.CreateLeadInput{
		FullName: strPtr("Anna Weber"), Source: "manual", ProjectID: &p.ID,
	})
	if err != nil {
		t.Fatalf("create lead on a project: %v", err)
	}
	if lead.ProjectId == nil || ids.UUID(*lead.ProjectId) != p.ID.UUID {
		t.Errorf("lead project = %v, want %v", lead.ProjectId, p.ID.UUID)
	}

	// PROJ-LIFE-2: a closed project still accepts work. Nothing about the
	// phase gates an attachment — only the auto-link ladder consults it.
	if _, err := e.Deals.AdvanceProjectPhase(e.Admin(), p.ID, deals.AdvanceProjectPhaseInput{
		ToPhase: deals.PhaseClosed, Reason: strPtr("Delivered."),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.People.CreateLead(e.Admin(), people.CreateLeadInput{
		FullName: strPtr("Late enquiry"), Source: "manual", ProjectID: &p.ID,
	}); err != nil {
		t.Errorf("a closed project refused a new lead: %v — phase is advisory, not a gate", err)
	}
}

// Filters are registered by the caller AFTER the shared list prelude is
// built, so every one of them must land in the same argument list the query
// is executed with. A prelude passed by value silently drops them and the
// query goes out short of placeholders — which fails as an opaque driver
// error, not as a wrong result.
func TestListProjectsAppliesFiltersRegisteredAfterThePrelude(t *testing.T) {
	e := Setup(t)
	wanted := e.SeedOrg(t, "BAER Pharma", nil)
	other := e.SeedOrg(t, "Kessler GmbH", nil)
	seedProject(e.Admin(), t, e, "ERP replacement", strPtr("ERP-27"), wanted, nil)
	seedProject(e.Admin(), t, e, "Rollout A", nil, other, nil)

	orgID := orgIDOf(wanted)
	byOrg, _, err := e.Deals.ListProjects(e.Admin(), deals.ListProjectsInput{OrganizationID: &orgID})
	if err != nil {
		t.Fatalf("list by organization: %v", err)
	}
	if len(byOrg) != 1 || byOrg[0].Name != "ERP replacement" {
		t.Errorf("organization filter returned %d rows, want only the anchored one", len(byOrg))
	}

	// Two filters plus a quick-find: three arguments registered after the
	// prelude, which is where the value-copy bug showed up.
	phase, query := deals.PhaseInitiative, "ERP"
	found, _, err := e.Deals.ListProjects(e.Admin(), deals.ListProjectsInput{
		OrganizationID: &orgID, Phase: &phase, Query: &query,
	})
	if err != nil {
		t.Fatalf("list by organization+phase+q: %v", err)
	}
	if len(found) != 1 {
		t.Errorf("combined filters returned %d rows, want 1", len(found))
	}

	// And the key lookup, matched case-insensitively like its index.
	key := "erp-27"
	byKey, _, err := e.Deals.ListProjects(e.Admin(), deals.ListProjectsInput{Key: &key})
	if err != nil {
		t.Fatalf("list by key: %v", err)
	}
	if len(byKey) != 1 {
		t.Errorf("key lookup returned %d rows, want 1", len(byKey))
	}
}

// The merge refusal is a read of both sides, so it obeys row scope like any
// other read. Work the caller cannot see must still BLOCK the merge —
// otherwise a rep quietly combines two companies whose projects another team
// owns — but it must not be NAMED, because naming a project is reading it.
func TestTheMergeRefusalCountsInvisibleProjectsWithoutNamingThem(t *testing.T) {
	e := Setup(t)
	source := e.SeedOrg(t, "Helios GmbH", nil)
	target := e.SeedOrg(t, "Helios AG", nil)
	// Each side's project belongs to a different rep, and the caller below
	// owns neither.
	seedProject(e.Admin(), t, e, "Secret migration", nil, source, &e.Rep1)
	seedProject(e.Admin(), t, e, "Secret rollout", nil, target, &e.Rep2)

	outsider := e.As(e.Rep3, []ids.UUID{e.Team2}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"organization": {Read: true, Update: true, Delete: true},
			"project":      {Read: true},
			"person":       {Read: true, Update: true},
		},
		RowScope: principal.RowScopeOwn,
	})

	_, err := e.People.MergeOrganization(outsider, orgIDOf(source), orgIDOf(target))
	var both *people.BothCompaniesCarryProjectsError
	if !errors.As(err, &both) {
		t.Fatalf("the merge produced %v, want a refusal — invisible work still blocks it", err)
	}
	// Blocked on the true state of the world...
	if both.SourceCount != 1 || both.TargetCount != 1 {
		t.Errorf("counted %d and %d live projects, want one each", both.SourceCount, both.TargetCount)
	}
	// ...and silent about work this caller may not read.
	if len(both.Source) != 0 || len(both.Target) != 0 {
		t.Errorf("the refusal named %v and %v to a caller who can see neither", both.Source, both.Target)
	}
	for _, secret := range []string{"Secret migration", "Secret rollout"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the refusal message leaked %q to a caller who cannot read it: %v", secret, err)
		}
	}
}

// The same refusal, seen by someone who owns both projects: it names them,
// because for this caller they are not a secret — the point of scoping the
// naming is precision, not silence.
func TestTheMergeRefusalNamesTheProjectsTheCallerCanSee(t *testing.T) {
	e := Setup(t)
	source := e.SeedOrg(t, "Vector Ltd", nil)
	target := e.SeedOrg(t, "Vector Limited", nil)
	seedProject(e.Admin(), t, e, "Mine A", nil, source, &e.Rep1)
	seedProject(e.Admin(), t, e, "Mine B", nil, target, &e.Rep1)

	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"organization": {Read: true, Update: true, Delete: true},
			"project":      {Read: true},
			"person":       {Read: true, Update: true},
		},
		RowScope: principal.RowScopeOwn,
	})

	_, err := e.People.MergeOrganization(owner, orgIDOf(source), orgIDOf(target))
	var both *people.BothCompaniesCarryProjectsError
	if !errors.As(err, &both) {
		t.Fatalf("the merge produced %v, want a refusal", err)
	}
	if len(both.Source) != 1 || both.Source[0] != "Mine A" {
		t.Errorf("source projects = %v, want the one this caller owns", both.Source)
	}
	if len(both.Target) != 1 || both.Target[0] != "Mine B" {
		t.Errorf("target projects = %v, want the one this caller owns", both.Target)
	}
}

// An activity's visibility DERIVES from its links, so replacing one is not a
// harmless association edit: cut the link a team sees the activity through
// and the activity leaves their world. Relink therefore replaces only what
// the caller can see.
//
// Scoping rather than refusing is deliberate — a refusal would confirm that
// an invisible link exists, which is precisely what the scope withholds.
func TestRelinkReplacesOnlyTheLinksTheCallerCanSee(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Contoso Werke", nil)
	theirs := seedProject(e.Admin(), t, e, "Their delivery", nil, org, &e.Rep1)
	mine := seedProject(e.Admin(), t, e, "My pursuit", nil, org, &e.Rep3)

	// One activity, linked to the other team's project and to a person the
	// attacker can see. The person link is how they reach the activity at all.
	person, err := e.People.CreatePerson(e.Admin(), people.CreatePersonInput{
		FullName: "Shared Contact", Source: "manual", OwnerID: userIDPtr(&e.Rep3),
	})
	if err != nil {
		t.Fatal(err)
	}
	act, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "note", Source: "manual",
		Links: []activities.ActivityLinkInput{
			{EntityType: "project", EntityID: theirs.ID.UUID},
			{EntityType: "person", EntityID: ids.UUID(person.Id)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	attacker := e.As(e.Rep3, []ids.UUID{e.Team2}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"activity": {Read: true, Update: true},
			"project":  {Read: true},
			"person":   {Read: true},
		},
		RowScope: principal.RowScopeOwn,
	})

	// Point it at their own project, asking to replace the existing one.
	_, err = e.Activities.RelinkActivity(attacker, ids.From[ids.ActivityKind](ids.UUID(act.Id)),
		activities.RelinkActivityInput{
			EntityType: "project", EntityID: mine.ID.UUID, ReplaceExistingOfType: true,
		})
	// Whether the insert succeeds is not the point — at most one project
	// link is allowed, so it may well refuse. What must hold either way is
	// that the link they could not see is still there.
	_ = err
	if n := e.WsCount(t, `
		SELECT count(*) FROM activity_link
		WHERE activity_id = $1 AND entity_type = 'project' AND project_id = $2`,
		ids.UUID(act.Id), theirs.ID.UUID); n != 1 {
		t.Fatalf("the other team's project link was removed by a caller who could not see it (%d remain)", n)
	}
}
