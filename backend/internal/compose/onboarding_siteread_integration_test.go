// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func onboardingDraft(t *testing.T, e *integration.Env) people.SiteRead {
	t.Helper()
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	read, joined, err := e.People.StartOnboardingSiteRead(ctx, seedURL, "human:"+e.Rep1.String(), nil)
	if err != nil {
		t.Fatalf("start onboarding read: %v", err)
	}
	if joined {
		t.Fatal("a fresh onboarding read joined an existing dossier")
	}
	if _, err := e.People.BeginSiteRead(deepReadWorkerCtx(context.Background(), SiteDeepReadArgs{
		WorkspaceID: e.WS, SiteReadID: read.ID, SeedURL: read.SeedURL, RequestedBy: read.RequestedBy,
	}), read.ID); err != nil {
		t.Fatalf("begin onboarding read: %v", err)
	}
	fields := []people.DeepReadField{
		{Field: "display_name", Value: "Acme", EvidenceSnippet: "Acme builds onboarding software.", SourceURL: seedURL, Confidence: 0.96},
		{Field: "offer_summary", Value: "Employee onboarding software", EvidenceSnippet: "Employee onboarding software for growing teams.", SourceURL: seedURL, Confidence: 0.91},
		{Field: "icp", Value: "Growing RevOps teams", EvidenceSnippet: "Built for growing RevOps teams.", SourceURL: seedURL, Confidence: 0.88},
	}
	facts := []people.DeepReadFact{
		{Category: "offering", Field: "service", Value: "Implementation — guided CRM rollout", ValueKey: "implementation", EvidenceSnippet: "Guided CRM rollout", SourceURL: seedURL, Confidence: 0.9},
		{Category: "signal", Field: "technology", Value: "PostgreSQL — data platform", ValueKey: "postgresql", EvidenceSnippet: "Built on PostgreSQL", SourceURL: seedURL, Confidence: 0.84},
	}
	found := []people.SiteReadPerson{{
		Name: "Anna Keller", Role: "Founder", EvidenceSnippet: "Anna Keller, Founder", SourceURL: seedURL + "/team",
	}}
	hash, err := siteReadProposalHash(fields, facts, found)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx := deepReadWorkerCtx(context.Background(), SiteDeepReadArgs{WorkspaceID: e.WS})
	if err := e.People.FinishSiteRead(workerCtx, read.ID, people.FinishSiteReadInput{
		Status: "done", FactCount: len(fields) + len(facts), ProfileFields: fields,
		Facts: facts, People: found, ProposalHash: hash,
	}); err != nil {
		t.Fatalf("finish onboarding read: %v", err)
	}
	ready, err := e.People.GetOnboardingSiteRead(ctx, read.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func TestOnboardingSiteReadConfirmsSelectedDataAndKeepsPeopleSeparate(t *testing.T) {
	e := integration.Setup(t)
	ready := onboardingDraft(t, e)
	if e.WsCount(t, `SELECT count(*) FROM organization WHERE is_anchor`) != 0 ||
		e.WsCount(t, `SELECT count(*) FROM organization_profile_field`) != 0 ||
		e.WsCount(t, `SELECT count(*) FROM organization_fact`) != 0 {
		t.Fatal("the operational onboarding draft wrote company domain truth before confirmation")
	}

	engine := &deepReadEngine{people: e.People, approvals: approvals.NewService(e.Pool)}
	offer, editedICP, website := "Employee onboarding software", "B2B RevOps teams with 50–500 employees", seedURL
	company, err := e.People.ConfirmCompanySiteRead(e.As(e.Rep1, nil, integration.AdminPerms), people.ConfirmCompanySiteReadInput{
		ReadID: ready.ID, DraftVersion: ready.DraftVersion, ProposalHash: ready.ProposalHash,
		DisplayName: "Acme", Website: &website,
		Fields:           map[string]*string{"offer_summary": &offer, "icp": &editedICP},
		SelectedFactKeys: []string{people.SiteReadFactKey(ready.Facts[0])},
	}, engine.stageOnboardingPeople)
	if err != nil {
		t.Fatalf("confirm onboarding read: %v", err)
	}
	if !company.MinimumComplete || len(company.Facts) != 1 || company.Facts[0].Field != "service" {
		t.Fatalf("confirmed company = %+v, want minimum-complete with only the selected service fact", company)
	}

	var siteRows, humanRows, leads, leadProposals int
	var confirmedOrg ids.UUID
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM organization_profile_field
			WHERE organization_id = $1 AND source = 'site_read' AND captured_by = 'agent:site-read'`, company.OrganizationID).Scan(&siteRows); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM organization_profile_field
			WHERE organization_id = $1 AND field = 'icp' AND source = 'human'`, company.OrganizationID).Scan(&humanRows); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM lead`).Scan(&leads); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM approval WHERE kind = 'site_lead'`).Scan(&leadProposals); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT organization_id FROM site_read WHERE id = $1 AND confirmed_at IS NOT NULL`, ready.ID).Scan(&confirmedOrg)
	})
	if err != nil {
		t.Fatal(err)
	}
	if siteRows != 2 || humanRows != 1 {
		t.Fatalf("profile provenance site/human = %d/%d, want 2/1", siteRows, humanRows)
	}
	if leads != 0 || leadProposals != 1 {
		t.Fatalf("people lane created %d leads and %d proposals, want 0 leads and 1 separate proposal", leads, leadProposals)
	}
	if confirmedOrg != company.OrganizationID.UUID {
		t.Fatalf("dossier bound to %s, want anchor %s", confirmedOrg, company.OrganizationID)
	}

	_, err = e.People.ConfirmCompanySiteRead(e.As(e.Rep1, nil, integration.AdminPerms), people.ConfirmCompanySiteReadInput{
		ReadID: ready.ID, DraftVersion: ready.DraftVersion, ProposalHash: ready.ProposalHash,
		DisplayName: "Acme", Fields: map[string]*string{"offer_summary": &offer, "icp": &editedICP},
	}, nil)
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("replayed confirmation = %v, want conflict", err)
	}
}

func TestOnboardingConfirmationRollsBackWhenSeparatePeopleCannotStage(t *testing.T) {
	e := integration.Setup(t)
	ready := onboardingDraft(t, e)
	offer, icp := "Employee onboarding software", "Growing RevOps teams"
	stageFailure := func(context.Context, pgx.Tx, ids.OrganizationID, people.SiteRead, []people.SiteReadPerson) ([]ids.UUID, error) {
		return nil, errors.New("approval store unavailable")
	}
	_, err := e.People.ConfirmCompanySiteRead(e.As(e.Rep1, nil, integration.AdminPerms), people.ConfirmCompanySiteReadInput{
		ReadID: ready.ID, DraftVersion: ready.DraftVersion, ProposalHash: ready.ProposalHash,
		DisplayName: "Acme", Fields: map[string]*string{"offer_summary": &offer, "icp": &icp},
	}, stageFailure)
	if err == nil {
		t.Fatal("confirmation succeeded while its separate people staging failed")
	}
	if e.WsCount(t, `SELECT count(*) FROM organization WHERE is_anchor`) != 0 ||
		e.WsCount(t, `SELECT count(*) FROM organization_profile_field`) != 0 ||
		e.WsCount(t, `SELECT count(*) FROM organization_fact`) != 0 {
		t.Fatal("a failed confirmation left partially committed company truth")
	}
	var confirmed int
	queryErr := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM site_read
			WHERE id = $1 AND confirmed_at IS NOT NULL`, ready.ID).Scan(&confirmed)
	})
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	if confirmed != 0 {
		t.Fatal("a failed confirmation marked the dossier confirmed")
	}
}

func TestOnboardingSiteReadStartRollsBackWhenQueueInsertFails(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	_, _, err := e.People.StartOnboardingSiteRead(ctx, seedURL, "human:"+e.Rep1.String(),
		func(context.Context, pgx.Tx, people.SiteRead) error {
			return errors.New("river insert failed")
		})
	if err == nil {
		t.Fatal("site-read start succeeded without its queue job")
	}
	if e.WsCount(t, `SELECT count(*) FROM site_read`) != 0 {
		t.Fatal("a failed queue insert left a queued dossier behind")
	}
}
