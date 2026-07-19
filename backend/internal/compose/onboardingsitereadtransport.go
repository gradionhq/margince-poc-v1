// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	siteReadStatusDeferred    = "deferred"
	siteReadWireStatusFailed  = "failed"
	siteReadWireStatusPartial = "partial"
)

func (e *deepReadEngine) startCompanySiteRead(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.StartCompanySiteReadRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	seedURL := strings.TrimSpace(req.Url)
	parsed, err := url.Parse(seedURL)
	if err != nil || (parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS) || parsed.Host == "" {
		httperr.Write(w, r, httperr.Validation("url", "invalid", "url must be an absolute http(s) URL"))
		return
	}
	read, _, err := e.people.StartOnboardingSiteRead(r.Context(), seedURL, requestedBy(r.Context()),
		func(ctx context.Context, tx pgx.Tx, read people.SiteRead) error {
			return e.enqueue.EnqueueTx(ctx, tx, SiteDeepReadArgs{
				WorkspaceID: storekit.MustWorkspace(ctx), SiteReadID: read.ID,
				SeedURL: read.SeedURL, RequestedBy: read.RequestedBy,
			}, siteDeepReadInsertOpts())
		})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/company/site-reads/"+read.ID.String())
	httperr.WriteJSON(w, http.StatusAccepted, companySiteRead(read))
}

func (e *deepReadEngine) getCompanySiteRead(w http.ResponseWriter, r *http.Request, readID openapi_types.UUID) {
	read, err := e.people.GetOnboardingSiteRead(r.Context(), ids.UUID(readID))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, companySiteRead(read))
}

func (e *deepReadEngine) confirmCompanySiteRead(w http.ResponseWriter, r *http.Request, readID openapi_types.UUID) {
	var req crmcontracts.ConfirmCompanySiteReadRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Profile.DisplayName) == "" {
		httperr.Write(w, r, httperr.Validation("profile.display_name", "empty", "a company needs a name"))
		return
	}
	for _, required := range []struct {
		field  string
		value  *string
		detail string
	}{
		{"profile.offer_summary", req.Profile.OfferSummary, "say what the company sells or delivers"},
		{"profile.icp", req.Profile.Icp, "say who the company sells to"},
	} {
		if !optionalFilled(required.value) {
			httperr.Write(w, r, httperr.Validation(required.field, "empty", required.detail))
			return
		}
	}
	website := trimOptional(req.Profile.Website)
	if website != nil && !parseableWebsite(*website) {
		httperr.Write(w, r, httperr.Validation("profile.website", "invalid", "website must be a domain or an absolute http(s) URL"))
		return
	}
	company, err := e.people.ConfirmCompanySiteRead(r.Context(), people.ConfirmCompanySiteReadInput{
		ReadID: ids.UUID(readID), DraftVersion: req.DraftVersion, ProposalHash: req.ProposalHash,
		DisplayName: strings.TrimSpace(req.Profile.DisplayName), Website: website,
		Fields: map[string]*string{
			fieldOfferSummary: trimOptional(req.Profile.OfferSummary), fieldLegalName: trimOptional(req.Profile.LegalName),
			fieldRegisteredAddress: trimOptional(req.Profile.RegisteredAddress), fieldRegisterVat: trimOptional(req.Profile.RegisterVat),
			fieldIndustry: trimOptional(req.Profile.Industry), fieldICP: trimOptional(req.Profile.Icp),
			fieldValueProposition: trimOptional(req.Profile.ValueProposition), fieldUSP: trimOptional(req.Profile.Usp),
			fieldCustomerPains: trimOptional(req.Profile.CustomerPains), fieldDesiredOutcomes: trimOptional(req.Profile.DesiredOutcomes),
			fieldBuyingCenter: trimOptional(req.Profile.BuyingCenter), fieldBuyingIntents: trimOptional(req.Profile.BuyingIntents),
			fieldCommonObjections: trimOptional(req.Profile.CommonObjections), fieldSalesMotion: trimOptional(req.Profile.SalesMotion),
			fieldHistory: trimOptional(req.Profile.History),
		},
		SelectedFactKeys: req.SelectedFactKeys,
	}, e.stageOnboardingPeople)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractCompany(company))
}

func (e *deepReadEngine) stageOnboardingPeople(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, read people.SiteRead, found []people.SiteReadPerson) ([]ids.UUID, error) {
	decider, ok := principal.Actor(ctx)
	if !ok {
		return nil, errors.New("compose: company site-read confirmation has no deciding principal")
	}
	execCtx := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "agent:site-read", UserID: decider.UserID, OnBehalfOf: decider.UserID,
	})
	proposalIDs := make([]ids.UUID, 0, len(found))
	for _, person := range found {
		in, err := siteLeadStageInput(read.ID, orgID.UUID, read.SeedURL, sitePerson{
			Name: person.Name, Role: person.Role, PublishedEmail: person.PublishedEmail,
			LinkedinURL: person.LinkedinURL, EvidenceSnippet: person.EvidenceSnippet, SourceURL: person.SourceURL,
		})
		if err != nil {
			return nil, err
		}
		id, err := e.approvals.StageInTx(execCtx, tx, in)
		if err != nil {
			return nil, err
		}
		proposalIDs = append(proposalIDs, id.UUID)
	}
	return proposalIDs, nil
}

func companySiteRead(read people.SiteRead) crmcontracts.CompanySiteRead {
	pages := make([]crmcontracts.CompanySiteReadPage, 0, len(read.Pages)+len(read.Skipped))
	for _, page := range read.Pages {
		kind := crmcontracts.CompanySiteReadPageKind(page.Kind)
		pages = append(pages, crmcontracts.CompanySiteReadPage{
			Url: page.URL, Status: crmcontracts.CompanySiteReadPageStatus("fetched"), Kind: &kind,
		})
	}
	for _, skip := range read.Skipped {
		reason := skip.Reason
		pages = append(pages, crmcontracts.CompanySiteReadPage{
			Url: skip.URL, Status: crmcontracts.CompanySiteReadPageStatus("skipped"), Reason: &reason,
		})
	}
	fields := make([]crmcontracts.ColdStartField, 0, len(read.ProfileFields))
	for _, field := range read.ProfileFields {
		sourceURL := field.SourceURL
		fields = append(fields, crmcontracts.ColdStartField{
			Field: crmcontracts.ColdStartFieldField(field.Field), Value: field.Value,
			EvidenceSnippet: field.EvidenceSnippet, SourceKind: crmcontracts.ColdStartFieldSourceKindUrl,
			SourceUrl: &sourceURL, Confidence: field.Confidence,
		})
	}
	facts := make([]crmcontracts.CompanySiteReadFact, 0, len(read.Facts))
	for _, fact := range read.Facts {
		facts = append(facts, crmcontracts.CompanySiteReadFact{
			Category: crmcontracts.CompanySiteReadFactCategory(fact.Category), Field: crmcontracts.CompanySiteReadFactField(fact.Field),
			Value: fact.Value, ValueKey: people.SiteReadFactKey(fact), EvidenceSnippet: fact.EvidenceSnippet,
			EvidenceUrl: fact.SourceURL, Confidence: fact.Confidence,
		})
	}
	found := make([]crmcontracts.CompanySiteReadPerson, 0, len(read.People))
	for _, person := range read.People {
		disposition := crmcontracts.CompanySiteReadPersonDisposition("separate_lead_proposal")
		out := crmcontracts.CompanySiteReadPerson{
			Name: person.Name, Role: person.Role, EvidenceSnippet: person.EvidenceSnippet,
			EvidenceUrl: person.SourceURL, Disposition: &disposition,
		}
		if person.PublishedEmail != "" {
			email := openapi_types.Email(person.PublishedEmail)
			out.PublishedEmail = &email
		}
		if person.LinkedinURL != "" {
			out.LinkedinUrl = &person.LinkedinURL
		}
		found = append(found, out)
	}
	status := map[string]string{
		"queued": "queued", siteReadStatusDeferred: siteReadStatusDeferred, "running": "reading", "done": "ready",
		siteReadWireStatusPartial: siteReadWireStatusPartial,
		siteReadWireStatusFailed:  siteReadWireStatusFailed,
	}[read.Status]
	if read.ConfirmedAt != nil {
		status = "confirmed"
	}
	out := crmcontracts.CompanySiteRead{
		Id: openapi_types.UUID(read.ID), TargetKind: crmcontracts.CompanySiteReadTargetKind("onboarding"),
		RootUrl: read.SeedURL, Status: crmcontracts.CompanySiteReadStatus(status), Pages: pages,
		ProfileFields: fields, Facts: facts, People: found, Warnings: read.Warnings,
		DraftVersion: read.DraftVersion, ProposalHash: read.ProposalHash,
		CreatedAt: read.CreatedAt, UpdatedAt: read.UpdatedAt, PagesRead: &read.PagesRead,
		StatusDetail: read.StatusDetail, NextAttemptAt: read.NextAttemptAt,
	}
	if read.StatusCode != nil {
		code := crmcontracts.CompanySiteReadStatusCode(*read.StatusCode)
		out.StatusCode = &code
	}
	if read.OrganizationID != nil {
		id := openapi_types.UUID(read.OrganizationID.UUID)
		out.OrganizationId = &id
	}
	if read.Phase != nil {
		phase := crmcontracts.CompanySiteReadPhase(*read.Phase)
		out.Phase = &phase
	}
	return out
}

func (h siteReadHandlers) StartCompanySiteRead(w http.ResponseWriter, r *http.Request, _ crmcontracts.StartCompanySiteReadParams) {
	if h.engine == nil {
		httperr.NotImplemented(w, r, "startCompanySiteRead (no crawl runner configured)")
		return
	}
	h.engine.startCompanySiteRead(w, r)
}

func (h siteReadHandlers) GetCompanySiteRead(w http.ResponseWriter, r *http.Request, readID openapi_types.UUID) {
	if h.engine == nil {
		httperr.NotImplemented(w, r, "getCompanySiteRead (no crawl runner configured)")
		return
	}
	h.engine.getCompanySiteRead(w, r, readID)
}

func (h siteReadHandlers) ConfirmCompanySiteRead(w http.ResponseWriter, r *http.Request, readID openapi_types.UUID, _ crmcontracts.ConfirmCompanySiteReadParams) {
	if h.engine == nil {
		httperr.NotImplemented(w, r, "confirmCompanySiteRead (no crawl runner configured)")
		return
	}
	h.engine.confirmCompanySiteRead(w, r, readID)
}
