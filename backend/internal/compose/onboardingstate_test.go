// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestOnboardingStateResponsePreservesResumeFields(t *testing.T) {
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	mode := identity.OnboardingSourceWebsite
	website := "https://gradion.com"
	displayName := "Gradion"
	readID := ids.MustParse("018f3a1b-0000-7000-8000-0000000000b2")
	state := identity.OnboardingState{
		Path: identity.OnboardingPathCreator, Step: identity.OnboardingStepConfirm,
		SourceMode: &mode, WebsiteURL: &website, SiteReadID: &readID,
		CompanyDraft:     identity.OnboardingCompanyDraft{DisplayName: &displayName},
		SelectedFactKeys: []string{"founded_year:2021"}, VoiceSkipped: true,
		Version: 7, CreatedAt: now, UpdatedAt: now,
	}

	response := onboardingStateResponse(state)
	if response.Path == nil || *response.Path != crmcontracts.Creator {
		t.Fatalf("path = %v, want creator", response.Path)
	}
	if response.Version == nil || *response.Version != 7 {
		t.Fatalf("version = %v, want 7", response.Version)
	}
	if response.SiteReadId == nil || response.SiteReadId.String() != readID.String() {
		t.Fatalf("site_read_id = %v, want %s", response.SiteReadId, readID)
	}
	if response.CompanyDraft.DisplayName == nil || *response.CompanyDraft.DisplayName != displayName {
		t.Fatalf("draft = %#v, want display name", response.CompanyDraft)
	}
	if len(response.SelectedFactKeys) != 1 || !response.VoiceSkipped {
		t.Fatalf("selection/skip = %#v/%t", response.SelectedFactKeys, response.VoiceSkipped)
	}
}

func TestOnboardingDraftInputMapsEveryNamedField(t *testing.T) {
	value := "grounded"
	draft := onboardingDraftInput(crmcontracts.OnboardingCompanyDraft{
		DisplayName: &value, OfferSummary: &value, Icp: &value,
		ValueProposition: &value, Usp: &value, CustomerPains: &value,
		DesiredOutcomes: &value, BuyingCenter: &value, BuyingIntents: &value,
		CommonObjections: &value, SalesMotion: &value, LegalName: &value,
		RegisteredAddress: &value, RegisterVat: &value, Industry: &value, History: &value,
	})

	if draft.DisplayName == nil || draft.History == nil || draft.RegisterVAT == nil {
		t.Fatalf("draft mapping dropped fields: %#v", draft)
	}
}
