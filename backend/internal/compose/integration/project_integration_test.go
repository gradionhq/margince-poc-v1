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
	"github.com/gradionhq/margince/backend/internal/platform/auth"
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

// The case that fails SILENTLY if the activity link-walk has no project
// branch: an activity linked ONLY to a project is reached through that
// project, so a seat that reads the project reads its conversation too.
// Getting this half-right looks exactly like missing data.
//
// A project is workspace-readable (platform/auth tableclass.go), so the walk
// admits every seat here rather than only the owner's team. What the walk
// still narrows is a link onto a capture-private record, which
// TestAMeetingNeverDisclosesTheRecordBehindALinkTheCallerCannotSee covers.
func TestAnActivityLinkedOnlyToAProjectIsReachedThroughIt(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	// Real seeded users: owner_id is a composite FK to app_user, so a
	// synthetic uuid would be refused before the scope rule is exercised.
	owner := e.Rep1
	colleague := e.Rep3
	p := seedProject(e.Admin(), t, e, "ERP replacement", nil, org, &owner)

	act, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "email", Subject: strPtr("rollout schedule"), Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "project", EntityID: p.ID.UUID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	activityID := ids.From[ids.ActivityKind](ids.UUID(act.Id))

	scoped := principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"project":  {Read: true},
			"activity": {Read: true},
		},
		RowScope: principal.RowScopeOwn,
	}
	// The project's owner sees the conversation about their project.
	if _, err := e.Activities.GetActivity(e.As(owner, nil, scoped), activityID, storekit.LiveOnly); err != nil {
		t.Errorf("the project's owner cannot see its activity: %v", err)
	}
	// So does a colleague who neither owns it nor was granted it: a project
	// is the workspace's to read, and its timeline travels with it.
	if _, err := e.Activities.GetActivity(e.As(colleague, nil, scoped), activityID, storekit.LiveOnly); err != nil {
		t.Errorf("a colleague on the project's timeline got %v; a project is read by every seat "+
			"holding the object grant, and the conversation about it is reached through it", err)
	}
	// A seat holding no ACTIVITY grant is refused the record type outright —
	// the link walk decides which rows, never whether the caller may read
	// activities at all.
	noActivity := principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects:  map[string]principal.ObjectGrant{"project": {Read: true}},
		RowScope: principal.RowScopeOwn,
	}
	_, err = e.Activities.GetActivity(e.As(colleague, nil, noActivity), activityID, storekit.LiveOnly)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("a seat with no activity grant got %v, want ErrPermissionDenied", err)
	}
}

// A rep who is neither owner nor stakeholder reads the project and its
// timeline whole, and still cannot change it. The read class and the write
// arm are separate rules (platform/auth tableclass.go vs writescope.go), and
// widening the first must not move the second: a consultant seeing the work
// is not a consultant who may rewrite it.
func TestARepReadsAProjectTheyDoNotOwnButCannotWriteIt(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	owner := e.Rep1
	// Rep3 sits in the other team, so neither own nor team scope reaches the
	// project — only the read class can.
	consultant := e.Rep3
	p := seedProject(e.Admin(), t, e, "ERP replacement", nil, org, &owner)

	act, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "note", Subject: strPtr("kickoff notes"), Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "project", EntityID: p.ID.UUID}},
	})
	if err != nil {
		t.Fatal(err)
	}

	reader := e.As(consultant, []ids.UUID{e.Team2}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"project":  {Read: true, Update: true},
			"activity": {Read: true},
		},
		RowScope: principal.RowScopeOwn,
	})

	got, err := e.Deals.GetProject(reader, p.ID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("a rep working a project they do not own cannot read it: %v", err)
	}
	if ids.UUID(got.Id) != p.ID.UUID {
		t.Fatalf("read back project %s, want %s", ids.UUID(got.Id), p.ID.UUID)
	}

	// The timeline travels with the record it hangs off.
	entityType, entityID := "project", p.ID.UUID
	timeline, _, err := e.Activities.ListActivities(reader, activities.ListActivitiesInput{
		EntityType: &entityType, EntityID: &entityID,
	})
	if err != nil {
		t.Fatalf("reading the project's timeline: %v", err)
	}
	if len(timeline) != 1 || ids.UUID(timeline[0].Id) != ids.UUID(act.Id) {
		t.Fatalf("the project timeline returned %d activities, want the one linked to it", len(timeline))
	}

	// The object grant says update; the ROW is still not theirs to change.
	// The refusal is permission-denied rather than not-found: this caller
	// legitimately reads the row, so there is no existence left to hide and
	// saying "not there" about a record they can see would be a lie.
	renamed := "Renamed by a passer-by"
	if _, err := e.Deals.UpdateProject(reader, p.ID, deals.UpdateProjectInput{Name: &renamed}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("a rep who does not own the project changed its name → %v, want ErrPermissionDenied — "+
			"reading a project is not permission to rewrite it", err)
	}
	// And the row really is untouched, so the refusal was not a rollback of a
	// write that had already landed.
	after, err := e.Deals.GetProject(e.Admin(), p.ID, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name == renamed {
		t.Fatal("the refused update still landed on the row")
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
// other read: work the caller cannot see must still BLOCK the merge —
// otherwise a rep quietly combines two companies whose projects another team
// owns — while a project it may not read is counted and not NAMED, because
// naming a project is reading it.
//
// Today every seat reads every project (platform/auth tableclass.go), so the
// refusal names both sides and withholds nothing. The withholding arm is
// asserted from the row-scope clause rather than hard-coded, so a project
// that ever returns to a scoped read re-arms this case instead of quietly
// turning the refusal into a disclosure.
func TestTheMergeRefusalCountsInvisibleProjectsWithoutNamingThem(t *testing.T) {
	e := Setup(t)
	// The merging rep owns both companies (a merge is a write, and an own-scope
	// seat only writes what it owns), but not the projects under them.
	source := e.SeedOrg(t, "Helios GmbH", &e.Rep3)
	target := e.SeedOrg(t, "Helios AG", &e.Rep3)
	// Each side's project belongs to a different rep, and the caller below
	// owns neither.
	seedProject(e.Admin(), t, e, "Secret migration", nil, source, &e.Rep1)
	seedProject(e.Admin(), t, e, "Secret rollout", nil, target, &e.Rep2)

	outsider := e.As(e.Rep3, []ids.UUID{e.Team2}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"organization":          {Read: true, Update: true, Delete: true},
			"project":               {Read: true},
			"person":                {Read: true, Update: true},
			"installation_settings": {Read: true},
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
	// ...and it names exactly the projects this caller may read. Ask the
	// row-scope class rather than assuming: while a project is workspace-read
	// both names belong in the message, and the day it is scoped again both
	// must disappear from it.
	readsEveryProject := auth.UnboundedFor(principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep3.String(), UserID: e.Rep3,
		Permissions: principal.Permissions{RowScope: principal.RowScopeOwn},
	}, "project")
	for _, secret := range []string{"Secret migration", "Secret rollout"} {
		named := strings.Contains(err.Error(), secret)
		if named != readsEveryProject {
			t.Errorf("the refusal names %q = %v, want %v — the message and the row-scope class "+
				"disagree, so it either leaks work this caller cannot read or withholds work "+
				"they can: %v", secret, named, readsEveryProject, err)
		}
	}
	if named := len(both.Source) + len(both.Target); (named == 2) != readsEveryProject {
		t.Errorf("the refusal named %v and %v; with workspace-read projects = %v that is the "+
			"wrong set", both.Source, both.Target, readsEveryProject)
	}
	// The SIZE of anything hidden is never reported. A number is a census:
	// repeat the merge against every company you can reach and the refusals
	// report how much hidden work each carries, and how it moves week to week.
	if !readsEveryProject && strings.ContainsAny(err.Error(), "0123456789") {
		t.Errorf("the refusal counted work the caller cannot see: %v", err)
	}
}

// The same refusal, seen by someone who owns both projects: it names them,
// because for this caller they are not a secret — the point of scoping the
// naming is precision, not silence.
func TestTheMergeRefusalNamesTheProjectsTheCallerCanSee(t *testing.T) {
	e := Setup(t)
	source := e.SeedOrg(t, "Vector Ltd", &e.Rep1)
	target := e.SeedOrg(t, "Vector Limited", &e.Rep1)
	seedProject(e.Admin(), t, e, "Mine A", nil, source, &e.Rep1)
	seedProject(e.Admin(), t, e, "Mine B", nil, target, &e.Rep1)

	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"organization":          {Read: true, Update: true, Delete: true},
			"project":               {Read: true},
			"person":                {Read: true, Update: true},
			"installation_settings": {Read: true},
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
//
// Organization links carry the test because several are allowed per activity:
// the replacement INSERT succeeds, so whatever the delete removed stays
// removed. On a one-per-activity type the insert refuses and the whole
// transaction rolls back, which would hide the difference this test exists to
// show. And an organization can be capture-private, which is what keeps a
// link invisible to a colleague — a deal is readable by every seat with the
// grant, so it can no longer stand in for a hidden link.
func TestRelinkReplacesOnlyTheLinksTheCallerCanSee(t *testing.T) {
	e := Setup(t)
	theirs := e.SeedOrg(t, "Their private account", &e.Rep1)
	mine := e.SeedOrg(t, "My account", &e.Rep3)
	person := e.SeedPerson(t, "Shared Contact", &e.Rep3)

	// One activity linked to the other rep's private account and to a person
	// the attacker owns. The person link is how they reach the activity at all.
	act, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "note", Source: "manual",
		Links: []activities.ActivityLinkInput{
			{EntityType: "organization", EntityID: theirs},
			{EntityType: "person", EntityID: person},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Made private once the link exists: the seeding admin is not the captor
	// and could not link to a private account.
	e.MakeCapturePrivate(t, "organization", theirs, e.Rep1)

	attacker := e.As(e.Rep3, []ids.UUID{e.Team2}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"activity":              {Read: true, Update: true},
			"organization":          {Read: true},
			"person":                {Read: true},
			"installation_settings": {Read: true},
		},
		RowScope: principal.RowScopeOwn,
	})

	if _, err := e.Activities.RelinkActivity(attacker, ids.From[ids.ActivityKind](ids.UUID(act.Id)),
		activities.RelinkActivityInput{
			EntityType: "organization", EntityID: mine, ReplaceExistingOfType: true,
		}); err != nil {
		t.Fatalf("relinking to an account the caller owns: %v", err)
	}

	// Their own link landed — so the write really happened and the delete
	// really ran, which is what makes the next assertion mean something.
	if n := e.WsCount(t, `
		SELECT count(*) FROM activity_link
		WHERE activity_id = $1 AND entity_type = 'organization' AND organization_id = $2`,
		ids.UUID(act.Id), mine); n != 1 {
		t.Fatalf("the caller's own relink did not land (%d links)", n)
	}
	// And the link they could never see is untouched.
	if n := e.WsCount(t, `
		SELECT count(*) FROM activity_link
		WHERE activity_id = $1 AND entity_type = 'organization' AND organization_id = $2`,
		ids.UUID(act.Id), theirs); n != 1 {
		t.Fatalf("the private account's link was removed by a caller who could not see it (%d remain)", n)
	}
}

// Moving an activity from one project to another is a plain success now, and
// that is the point worth pinning.
//
// The scoped delete in relink only removes links the caller can SEE, and at
// most one project link may exist per activity. While a project was row-
// scoped those two rules collided: replacing a project link the caller could
// not see refused where replacing nothing succeeded, and the difference told
// them a hidden link was there. One bit escaped, and it could not be closed
// from the relink path — hiding the link and enforcing one-per-activity were
// the same question asked twice.
//
// Making a project workspace-readable (platform/auth tableclass.go) closed it
// by removing the premise: there is no project link a seat holding the object
// grant cannot see, so the delete reaches every one of them and the move
// simply works. This case is the witness that the oracle is gone, and it
// fails the day a project link can be invisible again.
func TestMovingAnActivityBetweenProjectsReplacesTheLinkItCannotSee(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Oracle GmbH", nil)
	theirs := seedProject(e.Admin(), t, e, "Their delivery", nil, org, &e.Rep1)
	ours := seedProject(e.Admin(), t, e, "Our pursuit", nil, org, &e.Rep3)
	person := e.SeedPerson(t, "Reachable Contact", &e.Rep3)

	act, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "note", Source: "manual",
		Links: []activities.ActivityLinkInput{
			{EntityType: "project", EntityID: theirs.ID.UUID},
			{EntityType: "person", EntityID: person},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Rep3 owns neither the activity's current project nor sits in Rep1's
	// team: only the read class reaches that link.
	outsider := e.As(e.Rep3, []ids.UUID{e.Team2}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"activity": {Read: true, Update: true},
			"project":  {Read: true},
			"person":   {Read: true},
		},
		RowScope: principal.RowScopeOwn,
	})

	if _, err := e.Activities.RelinkActivity(outsider, ids.From[ids.ActivityKind](ids.UUID(act.Id)),
		activities.RelinkActivityInput{
			EntityType: "project", EntityID: ours.ID.UUID, ReplaceExistingOfType: true,
		}); err != nil {
		t.Fatalf("moving the activity onto another project: %v — the replace could not reach the "+
			"link it had to delete, which is the 23505 the one-project index raises", err)
	}
	// The move really happened: the old link is gone and the new one is there.
	if n := e.WsCount(t, `
		SELECT count(*) FROM activity_link
		WHERE activity_id = $1 AND entity_type = 'project' AND project_id = $2`,
		ids.UUID(act.Id), theirs.ID.UUID); n != 0 {
		t.Errorf("the displaced project link survived (%d remain)", n)
	}
	if n := e.WsCount(t, `
		SELECT count(*) FROM activity_link
		WHERE activity_id = $1 AND entity_type = 'project' AND project_id = $2`,
		ids.UUID(act.Id), ours.ID.UUID); n != 1 {
		t.Errorf("the new project link did not land (%d rows)", n)
	}
	// And exactly one project link remains, which is what the index is for.
	if n := e.WsCount(t, `
		SELECT count(*) FROM activity_link WHERE activity_id = $1 AND entity_type = 'project'`,
		ids.UUID(act.Id)); n != 1 {
		t.Errorf("project links = %d, want exactly 1", n)
	}
}

// The timeline filter and the timeline write must speak one vocabulary. They
// did not: writes accepted every link target while the filter knew only
// three, so an activity could be linked to a lead or a project and then be
// unfindable by the very link that had just been written.
func TestTheTimelineFilterKnowsEveryLinkTargetTheWriteAccepts(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Vocabulary GmbH", nil)
	project := seedProject(e.Admin(), t, e, "Findable work", nil, org, nil)
	person := e.SeedPerson(t, "Findable Contact", nil)

	act, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "note", Source: "manual",
		Links: []activities.ActivityLinkInput{
			{EntityType: "project", EntityID: project.ID.UUID},
			{EntityType: "person", EntityID: person},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Every kind the write took must answer a filter on that same kind.
	for kind, id := range map[string]ids.UUID{
		"project": project.ID.UUID,
		"person":  person,
	} {
		t.Run(kind, func(t *testing.T) {
			entityType := kind
			entityID := id
			found, _, err := e.Activities.ListActivities(e.Admin(), activities.ListActivitiesInput{
				EntityType: &entityType, EntityID: &entityID,
			})
			if err != nil {
				t.Fatalf("filtering the timeline by %s: %v", kind, err)
			}
			if len(found) != 1 || ids.UUID(found[0].Id) != ids.UUID(act.Id) {
				t.Fatalf("filtering by %s returned %d activities, want the one linked to it", kind, len(found))
			}
		})
	}
}
