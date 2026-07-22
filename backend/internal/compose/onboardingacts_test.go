// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

type onboardingVoiceReaderStub struct {
	profiles  []ai.VoiceProfile
	summary   ai.CorpusSummary
	candidate *int
	err       error
}

func (s onboardingVoiceReaderStub) ListProfiles(context.Context, *string, *int) (ai.VoiceProfilePage, error) {
	return ai.VoiceProfilePage{Items: s.profiles}, s.err
}

func (s onboardingVoiceReaderStub) ProfilePresentation(context.Context, ids.UUID) (ai.CorpusSummary, *int, error) {
	return s.summary, s.candidate, s.err
}

func TestSelectedOptionAuthorizesExactlyTheSelectedChange(t *testing.T) {
	base := newCompanyChangeAuthorization("Which one is right?", nil, "")
	granted := base.withSelectedOption("legal_name", "  Acme GmbH  ")
	tests := map[string]struct {
		authorization companyChangeAuthorization
		change        companyReadProposedChange
		want          bool
	}{
		"exact field and value": {
			authorization: granted,
			change:        companyReadProposedChange{Field: "legal_name", Value: "Acme GmbH"},
			want:          true,
		},
		"value with surrounding whitespace still matches": {
			authorization: granted,
			change:        companyReadProposedChange{Field: "legal_name", Value: " Acme GmbH "},
			want:          true,
		},
		"different value on the selected field is refused": {
			authorization: granted,
			change:        companyReadProposedChange{Field: "legal_name", Value: "Acme Holding AG"},
			want:          false,
		},
		"selected value on a different field is refused": {
			authorization: granted,
			change:        companyReadProposedChange{Field: "display_name", Value: "Acme GmbH"},
			want:          false,
		},
		"without a selection the question authorizes nothing": {
			authorization: base,
			change:        companyReadProposedChange{Field: "legal_name", Value: "Acme GmbH"},
			want:          false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.authorization.allows(tc.change); got != tc.want {
				t.Fatalf("allows(%+v) = %v, want %v", tc.change, got, tc.want)
			}
		})
	}
}

func TestSelectedOptionAuthorizesTheChangeEndToEnd(t *testing.T) {
	readID := ids.NewV7()
	brain := &validatedOnboardingBrainStub{response: model.Response{Text: `{
		"kind":"correction",
		"message":"I'll record Acme GmbH as the legal name.",
		"proposed_changes":[{"field":"legal_name","value":"Acme GmbH","reason":"You picked it from the legal notice.","source_ids":[]}],
		"source_ids":[]}`}}
	assistant := onboardingCompanyAssistant{
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7(), SiteReadID: &readID}},
		people: onboardingSiteReadReaderStub{read: people.SiteRead{ID: readID, Status: siteReadWireStatusDone, LegalEntities: []people.SiteReadLegalEntity{
			{Name: "Acme GmbH", SourceURL: "https://acme.example/impressum"},
			{Name: "Acme Holding AG", SourceURL: "https://acme.example/impressum"},
		}}},
		brain: brain, runtime: &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD"}},
	}
	recorder := onboardingCompanyRequest(&assistant, `{
		"message":"Which one is right?","locale":"en",
		"selected_option":{"clarify_id":"clarify:legal_name:0","field":"legal_name","value":"Acme GmbH"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.OnboardingCompanyMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.Kind != crmcontracts.CompanyConversationCorrection || len(reply.ProposedChanges) != 1 ||
		reply.ProposedChanges[0].Value != "Acme GmbH" || reply.Act != crmcontracts.OnboardingActCompany {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestOnboardingMessageRejectsInvalidActAndSelectionShapes(t *testing.T) {
	assistant := onboardingCompanyAssistant{
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}},
		brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{},
	}
	tests := map[string]string{
		"unknown act":                       `{"message":"Hello","locale":"en","act":"warmup"}`,
		"selection outside the company act": `{"message":"Hello","locale":"en","act":"voice","selected_option":{"clarify_id":"c","field":"legal_name","value":"Acme"}}`,
		"selection without a clarify id":    `{"message":"Hello","locale":"en","selected_option":{"clarify_id":" ","field":"legal_name","value":"Acme"}}`,
		"selection naming an unknown field": `{"message":"Hello","locale":"en","selected_option":{"clarify_id":"c","field":"favorite_color","value":"Acme"}}`,
		"selection without a value":         `{"message":"Hello","locale":"en","selected_option":{"clarify_id":"c","field":"legal_name","value":"  "}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if recorder := onboardingCompanyRequest(&assistant, body); recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCompanyActClarificationCarriesTheDetectedQuestion(t *testing.T) {
	readID := ids.NewV7()
	brain := &validatedOnboardingBrainStub{response: model.Response{Text: `{
		"kind":"clarification","message":"The legal notice names two entities.","proposed_changes":[],"source_ids":[]}`}}
	assistant := onboardingCompanyAssistant{
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7(), SiteReadID: &readID}},
		people: onboardingSiteReadReaderStub{read: people.SiteRead{ID: readID, Status: siteReadWireStatusDone, DraftVersion: 3, LegalEntities: []people.SiteReadLegalEntity{
			{Name: "Acme GmbH", SourceURL: "https://acme.example/impressum"},
			{Name: "Acme Holding AG", SourceURL: "https://acme.example/impressum"},
		}}},
		brain: brain, runtime: &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD"}},
	}
	recorder := onboardingCompanyRequest(&assistant, `{"message":"Which company am I?","locale":"en"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.OnboardingCompanyMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.Clarify == nil || reply.Clarify.Id != "clarify:legal_name:3" ||
		len(reply.Clarify.Options) != 2 || reply.Clarify.Options[0].Value != "Acme GmbH" {
		t.Fatalf("clarify = %+v", reply.Clarify)
	}
}

func TestVoiceActAnswersFromServerCorpusNumbersOnly(t *testing.T) {
	brain := &validatedOnboardingBrainStub{response: model.Response{Text: `{
		"kind":"answer","message":"Your corpus holds 1240 of your own words.","proposed_changes":[],"source_ids":[]}`}}
	assistant := onboardingCompanyAssistant{
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}},
		brain: brain, runtime: &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD"}},
		voice: onboardingVoiceReaderStub{
			profiles: []ai.VoiceProfile{{ID: ids.NewV7(), Status: "draft"}},
			summary:  ai.CorpusSummary{TotalWords: 1240, TargetWords: ai.CorpusTargetWords, SourceCount: 3, QualityBand: "starter", Maturity: "starter"},
		},
	}
	recorder := onboardingCompanyRequest(&assistant, `{"message":"How is my voice corpus doing?","locale":"en","act":"voice"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.OnboardingCompanyMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.Act != crmcontracts.OnboardingActVoice || len(reply.ProposedChanges) != 0 ||
		reply.AvailableAction == nil || *reply.AvailableAction != crmcontracts.OnboardingAvailableActionStartVoiceBuild {
		t.Fatalf("voice reply = %+v", reply)
	}
	if !strings.Contains(brain.request.Messages[0].Content, `"corpus_total_words":1240`) ||
		!strings.Contains(brain.request.System, "never invent a count") ||
		!strings.Contains(brain.request.System, "Respond in en.") || brain.request.SecretStripper == nil {
		t.Fatalf("voice model request = %+v", brain.request)
	}
}

func TestVoiceActBelowTheFloorOffersUploadInstead(t *testing.T) {
	brain := &validatedOnboardingBrainStub{response: model.Response{Text: `{
		"kind":"answer","message":"Add more of your own writing first.","proposed_changes":[],"source_ids":[]}`}}
	assistant := onboardingCompanyAssistant{
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}},
		brain: brain, runtime: &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD"}},
		voice: onboardingVoiceReaderStub{
			profiles: []ai.VoiceProfile{{ID: ids.NewV7(), Status: "draft"}},
			summary:  ai.CorpusSummary{TotalWords: ai.StarterVoiceWords - 1},
		},
	}
	recorder := onboardingCompanyRequest(&assistant, `{"message":"Can you build my voice now?","locale":"en","act":"voice"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.OnboardingCompanyMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.AvailableAction == nil || *reply.AvailableAction != crmcontracts.OnboardingAvailableActionUploadVoiceSource {
		t.Fatalf("below-floor voice reply = %+v", reply)
	}
}

func TestNonCompanyActsRefuseModelProposedChanges(t *testing.T) {
	for _, act := range []string{"voice", "results", "connect"} {
		t.Run(act, func(t *testing.T) {
			brain := &validatedOnboardingBrainStub{response: model.Response{Text: `{
				"kind":"recommendation","message":"Set the ICP.",
				"proposed_changes":[{"field":"icp","value":"Mid-market","reason":"Fits.","source_ids":[]}],
				"source_ids":[]}`}}
			assistant := onboardingCompanyAssistant{
				state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}},
				brain: brain, runtime: &onboardingRuntimeStub{},
				voice: onboardingVoiceReaderStub{},
			}
			recorder := onboardingCompanyRequest(&assistant, `{"message":"What should I do?","locale":"en","act":"`+act+`"}`)
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("a %s-act reply carrying company changes must be refused, got %d: %s", act, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestResultsAndConnectActsAnswerFromProgressContext(t *testing.T) {
	readID := ids.NewV7()
	confirmed := people.SiteRead{ID: readID, Status: siteReadWireStatusDone}
	now := confirmed.CreatedAt
	confirmed.ConfirmedAt = &now
	tests := map[string]struct {
		act        string
		wantAction crmcontracts.OnboardingCompanyMessageReplyAvailableAction
	}{
		"results": {act: "results", wantAction: crmcontracts.OnboardingAvailableActionFinish},
		"connect": {act: "connect", wantAction: crmcontracts.OnboardingAvailableActionConnectInbox},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			brain := &validatedOnboardingBrainStub{response: model.Response{Text: `{
				"kind":"answer","message":"Here is where you stand.","proposed_changes":[],"source_ids":[]}`}}
			assistant := onboardingCompanyAssistant{
				state:  onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7(), SiteReadID: &readID}},
				people: onboardingSiteReadReaderStub{read: confirmed},
				brain:  brain, runtime: &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD"}},
				voice: onboardingVoiceReaderStub{},
			}
			recorder := onboardingCompanyRequest(&assistant, `{"message":"Where do I stand?","locale":"en","act":"`+tc.act+`"}`)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var reply crmcontracts.OnboardingCompanyMessageReply
			if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
				t.Fatalf("decode reply: %v", err)
			}
			if string(reply.Act) != tc.act || reply.AvailableAction == nil || *reply.AvailableAction != tc.wantAction ||
				reply.NextRequiredField != nil {
				t.Fatalf("%s reply = %+v", tc.act, reply)
			}
			if !strings.Contains(brain.request.Messages[0].Content, `"company_confirmed":true`) {
				t.Fatalf("%s context = %s", tc.act, brain.request.Messages[0].Content)
			}
		})
	}
}
