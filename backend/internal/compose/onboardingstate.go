// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"errors"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// onboardingStateHandlers is the compose-owned orchestration edge: identity
// owns the per-user state row, while people owns the anchor whose existence
// decides creator/member routing and whose minimum gates creator advancement.
type onboardingStateHandlers struct {
	state   *identity.OnboardingStore
	company *people.Store
}

func (h onboardingStateHandlers) GetOnboardingState(w http.ResponseWriter, r *http.Request) {
	state, err := h.state.Get(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, onboardingStateResponse(state))
}

func (h onboardingStateHandlers) PutOnboardingState(
	w http.ResponseWriter,
	r *http.Request,
	_ crmcontracts.PutOnboardingStateParams,
) {
	var req crmcontracts.PutOnboardingStateRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	company, companyErr := h.company.GetCompany(r.Context())
	companyExists := companyErr == nil
	if companyErr != nil && !errors.Is(companyErr, apperrors.ErrNotFound) {
		httperr.Write(w, r, companyErr)
		return
	}

	in := identity.PutOnboardingStateInput{
		ExpectedVersion:  int64(req.ExpectedVersion),
		Step:             string(req.Step),
		CompanyDraft:     onboardingDraftInput(req.CompanyDraft),
		SelectedFactKeys: copyOnboardingFactKeys(req.SelectedFactKeys),
		VoiceSkipped:     req.VoiceSkipped,
		ConnectSkipped:   req.ConnectSkipped,
		CompanyExists:    companyExists,
		CompanyComplete:  companyExists && company.MinimumComplete,
	}
	if req.SourceMode != nil {
		mode := string(*req.SourceMode)
		in.SourceMode = &mode
	}
	if req.WebsiteUrl != nil {
		website := *req.WebsiteUrl
		in.WebsiteURL = &website
	}
	if req.SiteReadId != nil {
		readID := ids.UUID(*req.SiteReadId)
		in.SiteReadID = &readID
	}

	state, err := h.state.Put(r.Context(), in)
	if err != nil {
		var invalid *identity.InvalidOnboardingStateError
		if errors.As(err, &invalid) {
			httperr.Write(w, r, httperr.Validation(invalid.Field, "invalid", invalid.Reason))
			return
		}
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, onboardingStateResponse(state))
}

func copyOnboardingFactKeys(keys []string) []string {
	copied := make([]string, len(keys))
	copy(copied, keys)
	return copied
}

func onboardingDraftInput(in crmcontracts.OnboardingCompanyDraft) identity.OnboardingCompanyDraft {
	return identity.OnboardingCompanyDraft{
		DisplayName: in.DisplayName, OfferSummary: in.OfferSummary, ICP: in.Icp,
		ValueProposition: in.ValueProposition, USP: in.Usp, CustomerPains: in.CustomerPains,
		DesiredOutcomes: in.DesiredOutcomes, BuyingCenter: in.BuyingCenter,
		BuyingIntents: in.BuyingIntents, CommonObjections: in.CommonObjections,
		SalesMotion: in.SalesMotion, LegalName: in.LegalName,
		RegisteredAddress: in.RegisteredAddress, RegisterVAT: in.RegisterVat,
		Industry: in.Industry, History: in.History,
	}
}

func onboardingStateResponse(state identity.OnboardingState) crmcontracts.OnboardingState {
	path := crmcontracts.OnboardingStatePath(state.Path)
	version := int(state.Version)
	createdAt, updatedAt := state.CreatedAt, state.UpdatedAt
	out := crmcontracts.OnboardingState{
		Path: &path, Step: crmcontracts.OnboardingStateStep(state.Step),
		CompanyDraft:     onboardingDraftResponse(state.CompanyDraft),
		SelectedFactKeys: copyOnboardingFactKeys(state.SelectedFactKeys),
		VoiceSkipped:     state.VoiceSkipped, ConnectSkipped: state.ConnectSkipped,
		Version: &version, CompletedAt: state.CompletedAt, CreatedAt: &createdAt, UpdatedAt: &updatedAt,
	}
	if state.SourceMode != nil {
		mode := crmcontracts.OnboardingStateSourceMode(*state.SourceMode)
		out.SourceMode = &mode
	}
	if state.WebsiteURL != nil {
		website := *state.WebsiteURL
		out.WebsiteUrl = &website
	}
	if state.SiteReadID != nil {
		readID := openapi_types.UUID(*state.SiteReadID)
		out.SiteReadId = &readID
	}
	return out
}

func onboardingDraftResponse(in identity.OnboardingCompanyDraft) crmcontracts.OnboardingCompanyDraft {
	return crmcontracts.OnboardingCompanyDraft{
		DisplayName: in.DisplayName, OfferSummary: in.OfferSummary, Icp: in.ICP,
		ValueProposition: in.ValueProposition, Usp: in.USP, CustomerPains: in.CustomerPains,
		DesiredOutcomes: in.DesiredOutcomes, BuyingCenter: in.BuyingCenter,
		BuyingIntents: in.BuyingIntents, CommonObjections: in.CommonObjections,
		SalesMotion: in.SalesMotion, LegalName: in.LegalName,
		RegisteredAddress: in.RegisteredAddress, RegisterVat: in.RegisterVAT,
		Industry: in.Industry, History: in.History,
	}
}
