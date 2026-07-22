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
	"github.com/gradionhq/margince/backend/internal/modules/ai"
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
	httperr.WriteJSON(w, http.StatusAccepted, companySiteRead(read, nil, nil))
}

func (e *deepReadEngine) getCompanySiteRead(w http.ResponseWriter, r *http.Request, readID openapi_types.UUID) {
	read, comparisons, err := e.people.GetCompanySiteRead(r.Context(), ids.UUID(readID))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	var runtime *ai.RunSummary
	if e.runtime != nil {
		summary, runtimeErr := e.runtime.Get(r.Context(), ids.UUID(readID))
		if runtimeErr != nil {
			httperr.Write(w, r, runtimeErr)
			return
		}
		runtime = &summary
	}
	httperr.WriteJSON(w, http.StatusOK, companySiteRead(read, comparisons, runtime))
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
		Resolutions:      siteReadResolutions(req.Resolutions),
	}, e.stageOnboardingPeople)
	if err != nil {
		var invalid *people.InvalidSiteReadResolutionError
		if errors.As(err, &invalid) {
			httperr.Write(w, r, httperr.Validation("resolutions", "invalid", invalid.Reason))
			return
		}
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

func siteReadResolutions(in *[]crmcontracts.CompanySiteReadResolution) []people.SiteReadResolution {
	if in == nil {
		return nil
	}
	out := make([]people.SiteReadResolution, len(*in))
	for i, resolution := range *in {
		out[i] = people.SiteReadResolution{
			Key: resolution.Key, Action: string(resolution.Action), Value: resolution.Value,
		}
	}
	return out
}

func companySiteRead(read people.SiteRead, compared []people.SiteReadComparison, runtime *ai.RunSummary) crmcontracts.CompanySiteRead {
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
	entities := make([]crmcontracts.CompanySiteReadLegalEntity, 0, len(read.LegalEntities))
	for _, entity := range read.LegalEntities {
		wire := crmcontracts.CompanySiteReadLegalEntity{Name: entity.Name, SourceUrl: entity.SourceURL}
		// The optional details stay ABSENT rather than empty: "the page did
		// not state it" and "the page stated nothing" must not read alike.
		if entity.RegisteredAddress != "" {
			wire.RegisteredAddress = &entity.RegisteredAddress
		}
		if entity.RegisterNumber != "" {
			wire.RegisterNumber = &entity.RegisterNumber
		}
		if entity.EvidenceSnippet != "" {
			wire.EvidenceSnippet = &entity.EvidenceSnippet
		}
		entities = append(entities, wire)
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
	comparisons := contractSiteReadComparisons(compared)
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
		ProfileFields: fields, Facts: facts, Comparisons: comparisons, People: found,
		LegalEntities: &entities, Warnings: read.Warnings,
		DraftVersion: read.DraftVersion, ProposalHash: read.ProposalHash,
		CreatedAt: read.CreatedAt, UpdatedAt: read.UpdatedAt, PagesRead: &read.PagesRead,
		StatusDetail: read.StatusDetail, NextAttemptAt: read.NextAttemptAt,
	}
	attachCompanySiteReadOptionals(&out, read, runtime)
	return out
}

func attachCompanySiteReadOptionals(out *crmcontracts.CompanySiteRead, read people.SiteRead, runtime *ai.RunSummary) {
	if runtime != nil {
		mapped := contractRunSummary(*runtime)
		out.AiRuntime = &mapped
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
}

func contractRunSummary(summary ai.RunSummary) crmcontracts.AiRunSummary {
	models := make([]crmcontracts.AiRunModelUsage, 0, len(summary.Models))
	for _, usage := range summary.Models {
		models = append(models, crmcontracts.AiRunModelUsage{
			Task: usage.Task, Tier: usage.Tier, Provider: usage.Provider,
			ConfiguredModel: usage.ConfiguredModel, ServedModel: usage.ServedModel,
			CallAttempts: usage.CallAttempts, TokensIn: usage.TokensIn, TokensOut: usage.TokensOut,
			CachedTokens: usage.CachedTokens, CacheWriteTokens: usage.CacheWriteTokens,
			ReasoningTokens: usage.ReasoningTokens, LatencyMs: usage.LatencyMS,
			EstimatedCostMicrousd: usage.EstimatedCostMicroUSD, UnpricedCalls: usage.UnpricedCalls,
			LastUsedAt: usage.LastUsedAt,
		})
	}
	return crmcontracts.AiRunSummary{
		Currency:     crmcontracts.AiRunSummaryCurrency(summary.Currency),
		CallAttempts: summary.CallAttempts, TokensIn: summary.TokensIn, TokensOut: summary.TokensOut,
		LatencyMs: summary.LatencyMS, EstimatedCostMicrousd: summary.EstimatedCostMicroUSD,
		UnpricedCalls: summary.UnpricedCalls, Models: models,
	}
}

func contractSiteReadComparisons(compared []people.SiteReadComparison) []crmcontracts.CompanySiteReadComparison {
	out := make([]crmcontracts.CompanySiteReadComparison, 0, len(compared))
	for _, comparison := range compared {
		var source *crmcontracts.CompanySiteReadComparisonCurrentSource
		if comparison.CurrentSource != nil {
			value := crmcontracts.CompanySiteReadComparisonCurrentSource(*comparison.CurrentSource)
			source = &value
		}
		out = append(out, crmcontracts.CompanySiteReadComparison{
			Key: comparison.Key, ValueKind: crmcontracts.CompanySiteReadComparisonValueKind(comparison.ValueKind),
			Classification: crmcontracts.CompanySiteReadComparisonClassification(comparison.Classification),
			CurrentValue:   comparison.CurrentValue, CurrentSource: source, ProposedValue: comparison.ProposedValue,
		})
	}
	return out
}

func (h siteReadHandlers) StartCompanySiteRead(w http.ResponseWriter, r *http.Request, _ crmcontracts.StartCompanySiteReadParams) {
	if !companyContextReadEnabled(h.companyContextRollout) {
		httperr.NotImplemented(w, r, "startCompanySiteRead (company context read rollout is disabled)")
		return
	}
	if h.engine == nil {
		httperr.NotImplemented(w, r, "startCompanySiteRead (no crawl runner configured)")
		return
	}
	h.engine.startCompanySiteRead(w, r)
}

func (h siteReadHandlers) GetCompanySiteRead(w http.ResponseWriter, r *http.Request, readID openapi_types.UUID) {
	if !companyContextReadEnabled(h.companyContextRollout) {
		httperr.NotImplemented(w, r, "getCompanySiteRead (company context read rollout is disabled)")
		return
	}
	if h.engine == nil {
		httperr.NotImplemented(w, r, "getCompanySiteRead (no crawl runner configured)")
		return
	}
	h.engine.getCompanySiteRead(w, r, readID)
}

func (h siteReadHandlers) ConfirmCompanySiteRead(w http.ResponseWriter, r *http.Request, readID openapi_types.UUID, _ crmcontracts.ConfirmCompanySiteReadParams) {
	if !companyContextReadEnabled(h.companyContextRollout) {
		httperr.NotImplemented(w, r, "confirmCompanySiteRead (company context read rollout is disabled)")
		return
	}
	if h.engine == nil {
		httperr.NotImplemented(w, r, "confirmCompanySiteRead (no crawl runner configured)")
		return
	}
	h.engine.confirmCompanySiteRead(w, r, readID)
}
