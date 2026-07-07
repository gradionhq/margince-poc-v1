// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Authority-scope invariants that only bind if EVERY path honors them:
// an FK argument naming a row-scoped record is a READ of the target
// (deal organization/partner, organization parent); the approval
// surface honors the target row's own/team scope, not just object
// grants; rejecting is a decision and demands the same authority as
// approving; and a burst of undecidable stagings cannot starve older
// decidable rows out of the inbox. Each test pins a hole, not a happy
// path.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// repPermsWithOrg extends the rep fixture with organization grants for
// the FK-target tests.
func repPermsWithOrg() principal.Permissions {
	p := principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"person":       {Create: true, Read: true, Update: true},
			"organization": {Create: true, Read: true, Update: true},
			"deal":         {Create: true, Read: true, Update: true},
			"pipeline":     {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	}
	return p
}

// An FK argument to a row-scoped record is a read of that record: a rep
// must not be able to attach a deal or a child organization to an
// organization their row scope hides — RLS and composite FKs stop
// cross-tenant corruption, but only the visibility probe proves the
// caller could read the target.
func TestFKTargetsRequireRowScopeVisibility(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)

	foreignOrg := e.SeedOrg(t, "Their Org", &e.Rep3) // team2's record
	visibleOrg := e.SeedOrg(t, "Our Org", &e.Rep1)   // rep1's own
	myDeal := e.SeedDeal(t, "Mine", pipeline, open, &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, repPermsWithOrg())

	// Create paths.
	if _, err := e.Deals.CreateDeal(rep, deals.CreateDealInput{
		Name: "Sneaky", PipelineID: pipeline, StageID: open, OrganizationID: &foreignOrg,
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("CreateDeal with out-of-scope organization → %v, want ErrNotFound", err)
	}
	foreignParent := ids.From[ids.OrganizationKind](foreignOrg)
	if _, err := e.People.CreateOrganization(rep, people.CreateOrganizationInput{
		DisplayName: "Sneaky Child", ParentOrgID: &foreignParent,
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("CreateOrganization with out-of-scope parent → %v, want ErrNotFound", err)
	}

	// Update paths — organization, partner, and parent reattachment.
	if _, err := e.Deals.UpdateDeal(rep, myDeal, deals.UpdateDealInput{OrganizationID: &foreignOrg}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("UpdateDeal attaching out-of-scope organization → %v, want ErrNotFound", err)
	}
	if _, err := e.Deals.UpdateDeal(rep, myDeal, deals.UpdateDealInput{PartnerOrganizationID: &foreignOrg}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("UpdateDeal attaching out-of-scope partner → %v, want ErrNotFound", err)
	}
	if _, err := e.People.UpdateOrganization(rep, ids.From[ids.OrganizationKind](visibleOrg), people.UpdateOrganizationInput{ParentOrgID: &foreignParent}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("UpdateOrganization reparenting under out-of-scope org → %v, want ErrNotFound", err)
	}

	// The same references succeed when the target IS visible — the gate
	// narrows scope, it does not break the feature.
	if _, err := e.Deals.UpdateDeal(rep, myDeal, deals.UpdateDealInput{OrganizationID: &visibleOrg}); err != nil {
		t.Errorf("UpdateDeal attaching own-team organization → %v, want ok", err)
	}
}

func stageFor(t *testing.T, svc *approvals.Service, e *Env, kind string, targetType string, target ids.UUID) ids.ApprovalID {
	t.Helper()
	id, err := svc.Stage(e.AgentCtx(), approvals.StageInput{
		Kind: kind, ProposedChange: json.RawMessage(`{}`), DiffHash: "h-" + ids.NewV7().String(),
		TargetType: targetType, TargetID: target, Summary: "test staging",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Holding deal.update does not entitle a rep to see or decide a staged
// change against another team's deal: the approval surface applies the
// target row's own/team scope on list, get, approve AND reject — an
// undecidable approval reads as absent, in both directions.
func TestApprovalAuthorityHonorsTargetRowScope(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	svc := approvals.NewService(e.Pool)

	theirDeal := e.SeedDeal(t, "Theirs", pipeline, open, &e.Rep3) // team2's
	approvalID := stageFor(t, svc, e, "advance_deal", "deal", theirDeal)

	// rep1 holds deal.update but team1 scope: object grant yes, row no.
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)

	pending, err := svc.List(rep, strPtr("pending"), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range pending {
		if a.ID == approvalID {
			t.Error("List discloses an approval whose target row the caller cannot see")
		}
	}
	if _, err := svc.Get(rep, approvalID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("Get out-of-row-scope approval → %v, want ErrNotFound", err)
	}
	if _, err := svc.Decide(rep, approvalID, true, nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("approve out-of-row-scope approval → %v, want ErrNotFound", err)
	}
	if _, err := svc.Decide(rep, approvalID, false, strPtr("no")); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("reject out-of-row-scope approval → %v, want ErrNotFound (a reject is a decision too)", err)
	}

	// A human with no decision grant at all cannot reject by leaked UUID
	// either — even when the target row itself would be visible.
	viewer := e.As(e.Rep3, []ids.UUID{e.Team2}, ReadOnlyPerms)
	if _, err := svc.Decide(viewer, approvalID, false, strPtr("go away")); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("reject without decision grants → %v, want ErrNotFound", err)
	}

	// The teammate who owns the target CAN decide — in both directions.
	owner := e.As(e.Rep3, []ids.UUID{e.Team2}, RepPerms)
	if _, err := svc.Get(owner, approvalID); err != nil {
		t.Errorf("owner Get → %v, want ok", err)
	}
	if _, err := svc.Decide(owner, approvalID, false, strPtr("not now")); err != nil {
		t.Errorf("owner reject → %v, want ok", err)
	}
}

// A burst of stagings the caller cannot decide must not starve older
// decidable rows out of the inbox: List pages past the scan window until
// the display limit fills or the table is exhausted.
func TestApprovalListPagesPastUndecidableBurst(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	svc := approvals.NewService(e.Pool)

	myDeal := e.SeedDeal(t, "Mine", pipeline, open, &e.Rep1)
	theirDeal := e.SeedDeal(t, "Theirs", pipeline, open, &e.Rep3)

	// Oldest first: ONE staging rep1 can decide…
	visibleID := stageFor(t, svc, e, "advance_deal", "deal", myDeal)
	// …buried under a full scan window of stagings they cannot.
	for range 220 {
		stageFor(t, svc, e, "advance_deal", "deal", theirDeal)
	}

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	pending, err := svc.List(rep, strPtr("pending"), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range pending {
		if a.ID == visibleID {
			found = true
		}
		if a.ID != visibleID {
			t.Errorf("inbox discloses undecidable approval %s", a.ID)
		}
	}
	if !found {
		t.Error("the one decidable approval was starved out of the inbox by newer undecidable rows")
	}
}
