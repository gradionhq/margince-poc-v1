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
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	siteReadStatusDeferred = "deferred"
	siteReadWireStatusDone = "done"
	// A read that ended without a fault: stopped by a decision rather than by
	// something going wrong. A failure is something to investigate; this is not.
	siteReadWireStatusCancelled = "cancelled"
	siteReadWireStatusFailed    = "failed"
	siteReadWireStatusPartial   = "partial"
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
				Workspace: storekit.MustWorkspace(ctx), SiteReadID: read.ID,
				RequestedBy: read.RequestedBy,
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
	summary, hasRuntime, err := e.companySiteReadRuntime(r.Context(), ids.UUID(readID))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	var runtime *ai.RunSummary
	if hasRuntime {
		runtime = &summary
	}
	httperr.WriteJSON(w, http.StatusOK, companySiteRead(read, comparisons, runtime))
}

func (e *deepReadEngine) companySiteReadRuntime(ctx context.Context, readID ids.UUID) (ai.RunSummary, bool, error) {
	if e.runtime == nil {
		return ai.RunSummary{}, false, nil
	}
	summary, err := e.runtime.Get(ctx, readID)
	if err == nil {
		return summary, true, nil
	}
	if errors.Is(err, apperrors.ErrPermissionDenied) {
		return ai.RunSummary{}, false, err
	}
	e.logger().WarnContext(ctx, "AI runtime transparency unavailable", "read_id", readID, "err", err)
	return ai.RunSummary{}, false, nil
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
	company, unadoptedLogo, err := e.people.ConfirmCompanySiteRead(r.Context(), people.ConfirmCompanySiteReadInput{
		ReadID: ids.UUID(readID), DraftVersion: req.DraftVersion, ProposalHash: req.ProposalHash,
		DisplayName: strings.TrimSpace(req.Profile.DisplayName), Website: website,
		// The object store lives on this side of the seam, so the dossier
		// releases the mark its anchor declines only while somebody is here to
		// collect the bytes behind it.
		ReclaimUnadoptedLogo: e.blob != nil,
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
		httperr.Write(w, r, siteReadConfirmationRefusal(err))
		return
	}
	// After the commit, never inside it: a delete cannot be rolled back, so a
	// transaction that failed behind one would leave the anchor — or the dossier
	// — pointing at bytes nothing can serve. The committed row no longer names
	// these, and no record ever did.
	deleteUnreferencedLogo(r.Context(), e.blob, e.logger(), ids.UUID(readID), unadoptedLogo)
	httperr.WriteJSON(w, http.StatusOK, toContractCompany(company))
}

// siteReadConfirmationRefusal gives each way a confirmation can be refused its
// own contract code. All three are 409s and a client branches on the machine
// code alone (frontend.md), so sharing one code between them would leave it
// unable to tell "somebody already finished onboarding" from "this read has no
// draft yet" without refetching and re-deriving which it was. The third, a
// draft that moved under the reader, already has version_skew from the shared
// registry and is left to it.
func siteReadConfirmationRefusal(err error) error {
	switch {
	case errors.Is(err, people.ErrSiteReadAlreadyConfirmed):
		return &httperr.DetailedError{
			Status: http.StatusConflict, Code: "already_confirmed",
			Detail: "This website read was already confirmed. Open the company profile to see what it created.",
		}
	case errors.Is(err, people.ErrSiteReadNotConfirmable):
		return &httperr.DetailedError{
			Status: http.StatusConflict, Code: "not_confirmable",
			Detail: "This website read has no draft to confirm. Wait for it to finish, or start a new read.",
		}
	}
	return err
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
		// A published person the workspace already reaches by email is not a
		// decision — the same floor the crawl worker's staging applies.
		//
		// Asked as the CONFIRMING HUMAN, not as execCtx's system principal.
		// The answer decides whether a proposal reaches their inbox, so a
		// workspace-wide answer would let them learn which addresses exist on
		// records their own row scope hides. Under their scope such a record
		// reads as absent and they simply get the proposal.
		known, err := e.people.EmailAlreadyOnFileTx(ctx, tx, person.PublishedEmail)
		// A confirmer who may not read people is told nothing and gets the
		// proposal; asking on the system principal's authority instead is the
		// disclosure the human context is here to prevent.
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			known, err = false, nil
		}
		if err != nil {
			return nil, err
		}
		if known {
			continue
		}
		in, err := siteLeadStageInput(read.ID, orgID.UUID, read.SeedURL, sitePerson{
			Name: person.Name, Role: person.Role, PublishedEmail: person.PublishedEmail,
			LinkedinURL: person.LinkedinURL, EvidenceSnippet: person.EvidenceSnippet, SourceURL: person.SourceURL,
		})
		if err != nil {
			return nil, err
		}
		// The joining path, not StageInTx: two onboarding confirmations of the
		// same site before anyone decides must leave ONE question per person,
		// exactly as the crawl worker's staging does.
		id, err := e.approvals.StageOrJoinPendingInTx(execCtx, tx, in)
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
	entities := contractSiteReadLegalEntities(read.LegalEntities)
	found := contractSiteReadPeople(read.People)
	comparisons := contractSiteReadComparisons(compared)
	// Every terminal status the store can hold maps to something. A status
	// missing from this table renders as the empty string, which is not a
	// value the contract's enum has and tells a client nothing at all.
	status := map[string]string{
		"queued": "queued", siteReadStatusDeferred: siteReadStatusDeferred, "running": "reading", "done": "ready",
		siteReadWireStatusPartial:   siteReadWireStatusPartial,
		siteReadWireStatusFailed:    siteReadWireStatusFailed,
		siteReadWireStatusCancelled: string(crmcontracts.CompanySiteReadStatusAbandoned),
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
	if read.StoppedReason != nil {
		// A read that ended on a cap, a deadline or the budget stopped by
		// decision, not by fault. Without the reason a bounded read and a
		// broken one look alike, and the caller has nothing honest to say.
		reason := crmcontracts.CompanySiteReadStoppedReason(*read.StoppedReason)
		out.StoppedReason = &reason
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

func contractSiteReadLegalEntities(entities []people.SiteReadLegalEntity) []crmcontracts.CompanySiteReadLegalEntity {
	out := make([]crmcontracts.CompanySiteReadLegalEntity, 0, len(entities))
	for _, entity := range entities {
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
		out = append(out, wire)
	}
	return out
}

func contractSiteReadPeople(found []people.SiteReadPerson) []crmcontracts.CompanySiteReadPerson {
	out := make([]crmcontracts.CompanySiteReadPerson, 0, len(found))
	for _, person := range found {
		disposition := crmcontracts.CompanySiteReadPersonDisposition("separate_lead_proposal")
		wire := crmcontracts.CompanySiteReadPerson{
			Name: person.Name, Role: person.Role, EvidenceSnippet: person.EvidenceSnippet,
			EvidenceUrl: person.SourceURL, Disposition: &disposition,
		}
		if person.PublishedEmail != "" {
			email := openapi_types.Email(person.PublishedEmail)
			wire.PublishedEmail = &email
		}
		if person.LinkedinURL != "" {
			wire.LinkedinUrl = &person.LinkedinURL
		}
		out = append(out, wire)
	}
	return out
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
