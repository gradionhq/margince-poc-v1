// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func onboardingProposalRequest(engine *onboardingProposalEngine, locale *crmcontracts.GetOnboardingCompanyProposalParamsLocale) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/v1/onboarding/company/proposal", nil)
	recorder := httptest.NewRecorder()
	onboardingStateHandlers{proposal: engine}.GetOnboardingCompanyProposal(recorder, request,
		crmcontracts.GetOnboardingCompanyProposalParams{Locale: locale})
	return recorder
}

func TestOnboardingProposalServesTheDeterministicMapping(t *testing.T) {
	readID := ids.NewV7()
	current := "Acme Software"
	read := people.SiteRead{
		ID: readID, Status: siteReadWireStatusDone, DraftVersion: 7, ProposalHash: "hash-7",
		ProfileFields: []people.DeepReadField{
			{Field: "offer_summary", Value: "CRM software", EvidenceSnippet: "We build CRM software", SourceURL: "https://acme.example", Confidence: 0.9},
			{Field: "icp", Value: "Mid-market", EvidenceSnippet: "for mid-market teams", SourceURL: "https://acme.example", Confidence: 0.55},
			{Field: "industry", Value: "Software", EvidenceSnippet: "software", SourceURL: "https://acme.example", Confidence: 0.54},
			{Field: "usp", Value: "Fast", EvidenceSnippet: "  ", SourceURL: "https://acme.example", Confidence: 0.9},
		},
		Facts: []people.DeepReadFact{{
			Category: "offering", Field: "service", Value: "CRM rollout — implementation", ValueKey: "crm rollout",
			EvidenceSnippet: "CRM rollout", SourceURL: "https://acme.example/services", Confidence: 0.8,
		}},
		LegalEntities: []people.SiteReadLegalEntity{
			{Name: "Acme GmbH", SourceURL: "https://acme.example/impressum"},
			{Name: "Acme AG", SourceURL: "https://acme.example/impressum"},
		},
	}
	comparisons := []people.SiteReadComparison{{Key: "display_name", Classification: "human_conflict", CurrentValue: &current, ProposedValue: "Acme GmbH"}}
	engine := &onboardingProposalEngine{
		state: onboardingStateReaderStub{state: identity.OnboardingState{
			ID: ids.NewV7(), SiteReadID: &readID,
			CompanyDraft: identity.OnboardingCompanyDraft{DisplayName: stringPtr("Acme")},
		}},
		people:  onboardingSiteReadReaderStub{read: read, comparisons: comparisons},
		rollout: companyContextRolloutOnboarding,
	}

	recorder := onboardingProposalRequest(engine, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var proposal crmcontracts.OnboardingCompanyProposal
	if err := json.Unmarshal(recorder.Body.Bytes(), &proposal); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	if !proposal.Ready {
		t.Fatalf("a done read must be ready: %+v", proposal)
	}
	// The 0.54 field is below the cold-start floor and the blank-evidence
	// field violates evidence-or-omit; both stay out.
	if proposal.Fields == nil || len(*proposal.Fields) != 2 ||
		(*proposal.Fields)[0].Field != "offer_summary" || (*proposal.Fields)[1].Field != "icp" {
		t.Fatalf("proposal fields = %+v", proposal.Fields)
	}
	if proposal.Facts == nil || len(*proposal.Facts) != 1 || (*proposal.Facts)[0].ValueKey != "offering/service/crm rollout" {
		t.Fatalf("proposal facts = %+v", proposal.Facts)
	}
	if proposal.OpenQuestions == nil || len(*proposal.OpenQuestions) != 2 {
		t.Fatalf("open questions = %+v", proposal.OpenQuestions)
	}
	if proposal.RemainingRequiredFields == nil || len(*proposal.RemainingRequiredFields) != 2 ||
		(*proposal.RemainingRequiredFields)[0] != "offer_summary" || (*proposal.RemainingRequiredFields)[1] != "icp" {
		t.Fatalf("remaining required = %+v", proposal.RemainingRequiredFields)
	}
	if proposal.DraftVersion == nil || *proposal.DraftVersion != 7 ||
		proposal.ProposalHash == nil || *proposal.ProposalHash != "hash-7" {
		t.Fatalf("version pin = %+v / %+v", proposal.DraftVersion, proposal.ProposalHash)
	}
}

func TestOnboardingProposalDoesNotReAskAnsweredQuestions(t *testing.T) {
	readID := ids.NewV7()
	engine := &onboardingProposalEngine{
		state: onboardingStateReaderStub{state: identity.OnboardingState{
			ID: ids.NewV7(), SiteReadID: &readID,
			// The persisted draft carries an earlier authorized selection:
			// exactly one option value of the legal-entity question.
			CompanyDraft: identity.OnboardingCompanyDraft{LegalName: stringPtr("Acme GmbH")},
		}},
		people: onboardingSiteReadReaderStub{read: people.SiteRead{ID: readID, Status: siteReadWireStatusDone, LegalEntities: []people.SiteReadLegalEntity{
			{Name: "Acme GmbH", RegisteredAddress: "Berlin 1", SourceURL: "https://acme.example/legal"},
			{Name: "Acme Holding AG", RegisteredAddress: "Zug 2", SourceURL: "https://acme.example/legal"},
		}}},
		rollout: companyContextRolloutOnboarding,
	}
	recorder := onboardingProposalRequest(engine, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var proposal crmcontracts.OnboardingCompanyProposal
	if err := json.Unmarshal(recorder.Body.Bytes(), &proposal); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	// The answered legal-name question is gone; the untouched address
	// question stays open.
	if proposal.OpenQuestions == nil || len(*proposal.OpenQuestions) != 1 ||
		(*proposal.OpenQuestions)[0].Field != fieldRegisteredAddress {
		t.Fatalf("open questions = %+v", proposal.OpenQuestions)
	}
}

func TestOnboardingProposalSpeaksTheRequestedLocale(t *testing.T) {
	readID := ids.NewV7()
	engine := &onboardingProposalEngine{
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7(), SiteReadID: &readID}},
		people: onboardingSiteReadReaderStub{read: people.SiteRead{ID: readID, Status: siteReadWireStatusDone, LegalEntities: []people.SiteReadLegalEntity{
			{Name: "Acme GmbH", SourceURL: "https://acme.example/legal"},
			{Name: "Acme Holding AG", SourceURL: "https://acme.example/legal"},
		}}},
		rollout: companyContextRolloutOnboarding,
	}
	de := crmcontracts.OnboardingProposalLocaleDE
	recorder := onboardingProposalRequest(engine, &de)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var proposal crmcontracts.OnboardingCompanyProposal
	if err := json.Unmarshal(recorder.Body.Bytes(), &proposal); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	if proposal.OpenQuestions == nil || len(*proposal.OpenQuestions) != 1 ||
		(*proposal.OpenQuestions)[0].Question != clarifyEntityQuestion("de") {
		t.Fatalf("de open questions = %+v", proposal.OpenQuestions)
	}
	// Option values stay locale-invariant: they are the printed strings
	// the selection must echo verbatim.
	if (*proposal.OpenQuestions)[0].Options[0].Value != "Acme GmbH" {
		t.Fatalf("de option values = %+v", (*proposal.OpenQuestions)[0].Options)
	}

	unknown := crmcontracts.GetOnboardingCompanyProposalParamsLocale("fr")
	if recorder := onboardingProposalRequest(engine, &unknown); recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown locale status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestOnboardingProposalReportsAnUnfinishedRead(t *testing.T) {
	for _, status := range []string{"queued", siteReadStatusDeferred, "running"} {
		t.Run(status, func(t *testing.T) {
			readID := ids.NewV7()
			engine := &onboardingProposalEngine{
				state:   onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7(), SiteReadID: &readID}},
				people:  onboardingSiteReadReaderStub{read: people.SiteRead{ID: readID, Status: status}},
				rollout: companyContextRolloutOnboarding,
			}
			recorder := onboardingProposalRequest(engine, nil)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var proposal crmcontracts.OnboardingCompanyProposal
			if err := json.Unmarshal(recorder.Body.Bytes(), &proposal); err != nil {
				t.Fatalf("decode proposal: %v", err)
			}
			if proposal.Ready {
				t.Fatalf("an unfinished %s read must not be ready", status)
			}
		})
	}
}

func TestOnboardingProposalRefusesWhenThereIsNothingToProposeFrom(t *testing.T) {
	tests := map[string]struct {
		engine *onboardingProposalEngine
		want   int
	}{
		"no engine wired": {engine: nil, want: http.StatusNotImplemented},
		"rollout off": {
			engine: &onboardingProposalEngine{rollout: companyContextRolloutOff},
			want:   http.StatusNotImplemented,
		},
		"no state yet": {
			engine: &onboardingProposalEngine{
				state:   onboardingStateReaderStub{err: apperrors.ErrNotFound},
				rollout: companyContextRolloutOnboarding,
			},
			want: http.StatusNotFound,
		},
		"state without a read": {
			engine: &onboardingProposalEngine{
				state:   onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}},
				rollout: companyContextRolloutOnboarding,
			},
			want: http.StatusNotFound,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := onboardingProposalRequest(tc.engine, nil)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}

func TestOnboardingProposalPassesDependencyFailuresThrough(t *testing.T) {
	readID := ids.NewV7()
	engine := &onboardingProposalEngine{
		state:   onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7(), SiteReadID: &readID}},
		people:  onboardingSiteReadReaderStub{err: errors.New("dossier unavailable")},
		rollout: companyContextRolloutOnboarding,
	}
	if recorder := onboardingProposalRequest(engine, nil); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
