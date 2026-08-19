// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Qualify-to-deal through the real people→deals edge: the promotion, the
// deal and the contact's seat on it land in one transaction, and a deal the
// deals store refuses rolls the whole promotion back.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestQualifyOpensTheDealAndSeatsTheContactInOneTransaction(t *testing.T) {
	e := integration.Setup(t)
	admin := e.Admin()
	dealsStore := deals.NewStore(e.DB(), DealsInstallation())
	if err := dealsStore.SeedDefaults(admin); err != nil {
		t.Fatalf("seed default pipeline: %v", err)
	}
	peopleStore := people.NewStore(e.DB()).WithDealOpener(leadDealOpener{deals: dealsStore})

	email, company := "qualify@example.test", "2txt GmbH"
	lead, _, err := peopleStore.CreateLead(admin, people.CreateLeadInput{Email: &email, CompanyName: &company, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	leadID := ids.From[ids.LeadKind](ids.UUID(lead.Id))

	out, err := peopleStore.QualifyLead(admin, leadID, people.PromoteLeadInput{
		Trigger: "human_qualify", Deal: &people.QualifyDealInput{},
	})
	if err != nil {
		t.Fatalf("qualify with deal: %v", err)
	}
	if out.DealID == nil {
		t.Fatal("qualify answered no deal id")
	}
	deal, err := dealsStore.GetDeal(admin, ids.From[ids.DealKind](*out.DealID), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("the deal does not read back: %v", err)
	}
	if deal.Name != company {
		t.Errorf("deal name = %q, want the lead's company %q", deal.Name, company)
	}
	pipeline, err := dealsStore.DefaultPipeline(admin)
	if err != nil {
		t.Fatal(err)
	}
	if deal.PipelineId == nil || deal.StageId == nil || *deal.PipelineId != pipeline.Id || pipeline.Stages == nil || *deal.StageId != (*pipeline.Stages)[0].Id {
		t.Errorf("deal sits in pipeline %v stage %v, want the default pipeline's first open stage", deal.PipelineId, deal.StageId)
	}
	after, err := peopleStore.GetLead(admin, leadID, storekit.IncludeArchived)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != crmcontracts.LeadStatusPromoted || after.QualifiedDealId == nil || ids.UUID(*after.QualifiedDealId) != *out.DealID {
		t.Errorf("lead after qualify: status=%s qualified_deal_id=%v, want promoted pointing at %s", after.Status, after.QualifiedDealId, out.DealID)
	}
	// The contact sits on the deal, which is what keeps the undo honest:
	// demote refuses while the deal is live.
	if e.WsCount(t, `SELECT count(*) FROM relationship WHERE kind = 'deal_stakeholder' AND person_id = $1 AND deal_id = $2 AND archived_at IS NULL`,
		ids.UUID(out.Person.Id), *out.DealID) != 1 {
		t.Error("the qualified contact is not seated on the deal")
	}
	var hasDeal *people.PersonHasDealError
	if _, err := peopleStore.DemoteLead(admin, leadID, "changed my mind"); !errors.As(err, &hasDeal) {
		t.Errorf("demote with the qualified deal live err = %v, want PersonHasDealError", err)
	}
}

// The seat on the deal takes the same object admission CreateRelationship
// takes: a caller who may qualify and open deals but not create
// relationships is refused the whole qualify-with-deal, not handed an edge
// through a side door.
func TestQualifyWithDealNeedsTheRelationshipGrant(t *testing.T) {
	e := integration.Setup(t)
	admin := e.Admin()
	dealsStore := deals.NewStore(e.DB(), DealsInstallation())
	if err := dealsStore.SeedDefaults(admin); err != nil {
		t.Fatal(err)
	}
	peopleStore := people.NewStore(e.DB()).WithDealOpener(leadDealOpener{deals: dealsStore})
	email := "noseat@example.test"
	lead, _, err := peopleStore.CreateLead(admin, people.CreateLeadInput{Email: &email, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	narrow := e.As(ids.NewV7(), nil, principal.Permissions{
		RoleKeys: []string{"rep"}, RowScope: principal.RowScopeAll,
		Objects: map[string]principal.ObjectGrant{
			"lead":     {Create: true, Read: true, Update: true, Delete: true},
			"person":   {Create: true, Read: true, Update: true},
			"deal":     {Create: true, Read: true, Update: true},
			"pipeline": {Read: true},
		},
	})
	_, err = peopleStore.QualifyLead(narrow, ids.From[ids.LeadKind](ids.UUID(lead.Id)), people.PromoteLeadInput{
		Trigger: "human_qualify", Deal: &people.QualifyDealInput{},
	})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("qualify-with-deal without relationship:create err = %v, want ErrPermissionDenied", err)
	}
	if _, err := peopleStore.GetLead(admin, ids.From[ids.LeadKind](ids.UUID(lead.Id)), storekit.LiveOnly); err != nil {
		t.Errorf("the refused qualify should have left the lead live and open: %v", err)
	}
}

func TestQualifyRollsBackWhenTheDealIsRefused(t *testing.T) {
	e := integration.Setup(t)
	admin := e.Admin()
	dealsStore := deals.NewStore(e.DB(), DealsInstallation())
	if err := dealsStore.SeedDefaults(admin); err != nil {
		t.Fatal(err)
	}
	peopleStore := people.NewStore(e.DB()).WithDealOpener(leadDealOpener{deals: dealsStore})
	email := "rollback@example.test"
	lead, _, err := peopleStore.CreateLead(admin, people.CreateLeadInput{Email: &email, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	leadID := ids.From[ids.LeadKind](ids.UUID(lead.Id))

	// A stage that belongs to no pipeline: the deals store refuses the birth.
	ghost := ids.NewV7()
	pipeline, err := dealsStore.DefaultPipeline(admin)
	if err != nil {
		t.Fatal(err)
	}
	pipelineID := ids.UUID(pipeline.Id)
	_, err = peopleStore.QualifyLead(admin, leadID, people.PromoteLeadInput{
		Trigger: "human_qualify", Deal: &people.QualifyDealInput{PipelineID: &pipelineID, StageID: &ghost},
	})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("qualify with a ghost stage err = %v, want ErrNotFound from the deal birth", err)
	}
	after, err := peopleStore.GetLead(admin, leadID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("the lead should still be live and open after the rollback: %v", err)
	}
	if after.Status != crmcontracts.LeadStatusNew || after.PromotedPersonId != nil || after.QualifiedDealId != nil {
		t.Errorf("lead after a refused deal: %+v, want untouched", after)
	}
	if e.WsCount(t, `SELECT count(*) FROM person WHERE converted_from_lead_id = $1`, ids.UUID(lead.Id)) != 0 {
		t.Error("a person survived the rolled-back promotion")
	}

	// A stage alone is placed in its own pipeline, not the default one.
	other, err := dealsStore.CreatePipeline(admin, deals.CreatePipelineInput{Name: "Partners", Stages: []deals.StageInput{
		{Name: "Intro", Position: 1, Semantic: "open", WinProbability: 10},
		{Name: "Won", Position: 2, Semantic: "won", WinProbability: 100},
		{Name: "Lost", Position: 3, Semantic: "lost", WinProbability: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	intro := ids.UUID((*other.Stages)[0].Id)
	stageEmail := "stage-only@example.test"
	stageLead, _, err := peopleStore.CreateLead(admin, people.CreateLeadInput{Email: &stageEmail, Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := peopleStore.QualifyLead(admin, ids.From[ids.LeadKind](ids.UUID(stageLead.Id)), people.PromoteLeadInput{
		Trigger: "human_qualify", Deal: &people.QualifyDealInput{StageID: &intro},
	})
	if err != nil {
		t.Fatalf("qualify with a stage alone: %v", err)
	}
	placed, err := dealsStore.GetDeal(admin, ids.From[ids.DealKind](*out.DealID), storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if placed.PipelineId == nil || *placed.PipelineId != other.Id || placed.StageId == nil || ids.UUID(*placed.StageId) != intro {
		t.Errorf("stage-only qualify put the deal in pipeline %v stage %v, want the stage's own pipeline %s / %s", placed.PipelineId, placed.StageId, other.Id, intro)
	}

	// Without the edge wired, asking for a deal is refused outright rather
	// than promoting without one.
	unwired := people.NewStore(e.DB())
	var notWired *people.DealOpenerNotWiredError
	if _, err := unwired.QualifyLead(admin, leadID, people.PromoteLeadInput{Trigger: "human_qualify", Deal: &people.QualifyDealInput{}}); !errors.As(err, &notWired) {
		t.Errorf("unwired qualify-with-deal err = %v, want DealOpenerNotWiredError", err)
	}
}
