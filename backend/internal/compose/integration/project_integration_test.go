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
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
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

func seedProject(t *testing.T, e *Env, ctx context.Context, name string, key *string, org ids.UUID, owner *ids.UUID) projectFixture {
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
	p := seedProject(t, e, e.Admin(), "ERP replacement", strPtr("ERP-27"), org, nil)

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
	first := seedProject(t, e, e.Admin(), "ERP replacement", strPtr("ERP-27"), org, nil)

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
	p := seedProject(t, e, e.Admin(), "ERP replacement", nil, org, nil)

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
	p := seedProject(t, e, e.Admin(), "ERP replacement", nil, org, nil)

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
	p := seedProject(t, e, e.Admin(), "ERP replacement", nil, orgA, nil)

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
	p := seedProject(t, e, e.Admin(), "ERP replacement", strPtr("ERP-27"), org, nil)

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
	p := seedProject(t, e, e.Admin(), "ERP replacement", nil, org, &ownerA)

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
