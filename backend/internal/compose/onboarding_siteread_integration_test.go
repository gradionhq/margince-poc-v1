// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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
	return finishOnboardingDraft(t, e, read)
}

func finishOnboardingDraft(t *testing.T, e *integration.Env, read people.SiteRead) people.SiteRead {
	t.Helper()
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
		Name: "Anna Keller", Role: "Founder", PublishedEmail: "anna@acme.example",
		LinkedinURL:     "https://www.linkedin.com/in/anna-keller",
		EvidenceSnippet: "Anna Keller, Founder", SourceURL: seedURL + "/team",
	}}
	hash, err := siteReadProposalHash(fields, facts, found)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx := deepReadWorkerCtx(context.Background(), SiteDeepReadArgs{WorkspaceID: e.WS})
	stopped := "page_cap"
	if err := e.People.FinishSiteRead(workerCtx, read.ID, people.FinishSiteReadInput{
		Status: "partial", FactCount: len(fields) + len(facts), ProfileFields: fields,
		Pages: []people.SiteReadPage{
			{URL: seedURL, Kind: "home"},
			{URL: seedURL + "/team", Kind: "team"},
		},
		Skipped:       []people.SiteReadSkip{{URL: seedURL + "/blog", Reason: "page_cap"}},
		StoppedReason: &stopped, Facts: facts, People: found,
		Warnings: []string{"Page limit reached."}, ProposalHash: hash,
	}); err != nil {
		t.Fatalf("finish onboarding read: %v", err)
	}
	ready, err := e.People.GetOnboardingSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), read.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func onboardingPOST(ctx context.Context, t *testing.T, path string, body any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)).WithContext(ctx)
}

func TestOnboardingSiteReadTransportStartsPollsAndConfirmsTheDraft(t *testing.T) {
	e := integration.Setup(t)
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	inserter := &fakeInserter{}
	engine := newDeepReadTestEngine(e, inserter)
	engine.approvals = approvals.NewService(e.Pool)

	start := onboardingPOST(human, t, "/v1/company/site-reads",
		crmcontracts.StartCompanySiteReadRequest{Url: "  " + seedURL + "  "})
	startRec := httptest.NewRecorder()
	engine.startCompanySiteRead(startRec, start)
	if startRec.Code != http.StatusAccepted {
		t.Fatalf("start → %d %s, want 202", startRec.Code, startRec.Body.String())
	}
	var started crmcontracts.CompanySiteRead
	if err := json.Unmarshal(startRec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.Status != crmcontracts.CompanySiteReadStatusQueued ||
		startRec.Header().Get("Location") != "/v1/company/site-reads/"+started.Id.String() || len(inserter.inserts) != 1 {
		t.Fatalf("started dossier = %+v, location %q, jobs %d", started, startRec.Header().Get("Location"), len(inserter.inserts))
	}

	read, err := e.People.GetOnboardingSiteRead(human, ids.UUID(started.Id))
	if err != nil {
		t.Fatal(err)
	}
	ready := finishOnboardingDraft(t, e, read)
	pollRec := httptest.NewRecorder()
	poll := httptest.NewRequest(http.MethodGet, "/v1/company/site-reads/"+ready.ID.String(), nil).WithContext(human)
	engine.getCompanySiteRead(pollRec, poll, openapi_types.UUID(ready.ID))
	if pollRec.Code != http.StatusOK {
		t.Fatalf("poll → %d %s, want 200", pollRec.Code, pollRec.Body.String())
	}
	var dossier crmcontracts.CompanySiteRead
	if err := json.Unmarshal(pollRec.Body.Bytes(), &dossier); err != nil {
		t.Fatal(err)
	}
	if dossier.Status != crmcontracts.CompanySiteReadStatusPartial || len(dossier.Pages) != 3 ||
		len(dossier.ProfileFields) != 3 || len(dossier.Facts) != 2 || len(dossier.People) != 1 ||
		dossier.People[0].PublishedEmail == nil || dossier.People[0].LinkedinUrl == nil {
		t.Fatalf("polled dossier lost progressive findings: %+v", dossier)
	}

	offer, icp, website := "Employee onboarding software", "Growing RevOps teams", seedURL
	confirmBody := crmcontracts.ConfirmCompanySiteReadRequest{
		DraftVersion: ready.DraftVersion,
		ProposalHash: ready.ProposalHash,
		Profile: crmcontracts.CompanyProfileInput{
			DisplayName: "Acme", OfferSummary: &offer, Icp: &icp, Website: &website,
		},
		SelectedFactKeys: []string{people.SiteReadFactKey(ready.Facts[0])},
	}
	confirm := onboardingPOST(human, t,
		"/v1/company/site-reads/"+ready.ID.String()+"/confirm", confirmBody)
	confirmRec := httptest.NewRecorder()
	engine.confirmCompanySiteRead(confirmRec, confirm, openapi_types.UUID(ready.ID))
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("confirm → %d %s, want 200", confirmRec.Code, confirmRec.Body.String())
	}

	confirmedRec := httptest.NewRecorder()
	confirmedPoll := httptest.NewRequest(http.MethodGet,
		"/v1/company/site-reads/"+ready.ID.String(), nil).WithContext(human)
	engine.getCompanySiteRead(confirmedRec, confirmedPoll, openapi_types.UUID(ready.ID))
	var confirmed crmcontracts.CompanySiteRead
	if err := json.Unmarshal(confirmedRec.Body.Bytes(), &confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != crmcontracts.CompanySiteReadStatusConfirmed || confirmed.OrganizationId == nil {
		t.Fatalf("confirmed dossier = %+v, want confirmed and bound", confirmed)
	}
}

func TestOnboardingSiteReadTransportRejectsInvalidManualInputs(t *testing.T) {
	e := integration.Setup(t)
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	engine := newDeepReadTestEngine(e, &fakeInserter{})

	badStart := onboardingPOST(human, t, "/v1/company/site-reads",
		crmcontracts.StartCompanySiteReadRequest{Url: "mailto:team@acme.example"})
	badStartRec := httptest.NewRecorder()
	engine.startCompanySiteRead(badStartRec, badStart)
	if badStartRec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid URL start → %d, want 422", badStartRec.Code)
	}

	offer, icp, badWebsite := "Employee onboarding software", "Growing RevOps teams", "http://"
	cases := []crmcontracts.CompanyProfileInput{
		{DisplayName: "", OfferSummary: &offer, Icp: &icp},
		{DisplayName: "Acme", Icp: &icp},
		{DisplayName: "Acme", OfferSummary: &offer},
		{DisplayName: "Acme", OfferSummary: &offer, Icp: &icp, Website: &badWebsite},
	}
	for i, profile := range cases {
		req := onboardingPOST(human, t, "/v1/company/site-reads/missing/confirm",
			crmcontracts.ConfirmCompanySiteReadRequest{
				DraftVersion: 1, ProposalHash: "hash", Profile: profile, SelectedFactKeys: []string{},
			})
		rec := httptest.NewRecorder()
		engine.confirmCompanySiteRead(rec, req, openapi_types.UUID(ids.NewV7()))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid confirmation %d → %d, want 422", i, rec.Code)
		}
	}

	missingID := openapi_types.UUID(ids.NewV7())
	missingRec := httptest.NewRecorder()
	missingPoll := httptest.NewRequest(http.MethodGet, "/v1/company/site-reads/missing", nil).WithContext(human)
	engine.getCompanySiteRead(missingRec, missingPoll, missingID)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing poll → %d, want 404", missingRec.Code)
	}

	valid := crmcontracts.CompanyProfileInput{DisplayName: "Acme", OfferSummary: &offer, Icp: &icp}
	missingConfirm := onboardingPOST(human, t, "/v1/company/site-reads/missing/confirm",
		crmcontracts.ConfirmCompanySiteReadRequest{
			DraftVersion: 1, ProposalHash: "hash", Profile: valid, SelectedFactKeys: []string{},
		})
	missingConfirmRec := httptest.NewRecorder()
	engine.confirmCompanySiteRead(missingConfirmRec, missingConfirm, missingID)
	if missingConfirmRec.Code != http.StatusNotFound {
		t.Fatalf("missing confirmation → %d, want 404", missingConfirmRec.Code)
	}

	brokenQueue := newDeepReadTestEngine(e, &fakeInserter{err: errors.New("queue unavailable")})
	queueRequest := onboardingPOST(human, t, "/v1/company/site-reads",
		crmcontracts.StartCompanySiteReadRequest{Url: seedURL})
	queueRec := httptest.NewRecorder()
	brokenQueue.startCompanySiteRead(queueRec, queueRequest)
	if queueRec.Code != http.StatusInternalServerError {
		t.Fatalf("broken queue start → %d, want 500", queueRec.Code)
	}

	malformed := httptest.NewRequest(http.MethodPost, "/v1/company/site-reads", bytes.NewBufferString("{"))
	malformedRec := httptest.NewRecorder()
	engine.startCompanySiteRead(malformedRec, malformed.WithContext(human))
	if malformedRec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed start → %d, want 422", malformedRec.Code)
	}
}

func TestOnboardingSiteReadHandlersStayExplicitWithoutAConfiguredEngine(t *testing.T) {
	handlers := siteReadHandlers{}
	readID := openapi_types.UUID(ids.NewV7())
	tests := []func(http.ResponseWriter, *http.Request){
		func(w http.ResponseWriter, r *http.Request) {
			handlers.StartCompanySiteRead(w, r, crmcontracts.StartCompanySiteReadParams{})
		},
		func(w http.ResponseWriter, r *http.Request) { handlers.GetCompanySiteRead(w, r, readID) },
		func(w http.ResponseWriter, r *http.Request) {
			handlers.ConfirmCompanySiteRead(w, r, readID, crmcontracts.ConfirmCompanySiteReadParams{})
		},
	}
	for i, invoke := range tests {
		rec := httptest.NewRecorder()
		invoke(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("unconfigured handler %d → %d, want 501", i, rec.Code)
		}
	}
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
