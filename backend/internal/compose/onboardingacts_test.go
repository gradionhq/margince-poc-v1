// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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

type onboardingCompanyReaderStub struct {
	company people.Company
	err     error
}

func (s onboardingCompanyReaderStub) GetCompany(context.Context) (people.Company, error) {
	return s.company, s.err
}

func TestVerifySelectedOptionBindsTheGrantToTheCurrentClarifications(t *testing.T) {
	current := "Acme Software"
	read := &people.SiteRead{DraftVersion: 3, LegalEntities: []people.SiteReadLegalEntity{
		{Name: "Acme GmbH", SourceURL: "https://acme.example/legal"},
		{Name: "Acme Holding AG", SourceURL: "https://acme.example/legal"},
	}}
	comparisons := []people.SiteReadComparison{{Key: "display_name", Classification: "human_conflict", CurrentValue: &current, ProposedValue: "Acme GmbH"}}
	selection := func(id, field, value string) crmcontracts.OnboardingClarifySelection {
		return crmcontracts.OnboardingClarifySelection{ClarifyId: id, Field: field, Value: value}
	}
	tests := map[string]struct {
		selection crmcontracts.OnboardingClarifySelection
		read      *people.SiteRead
		wantOK    bool
	}{
		"listed entity option passes":             {selection: selection("clarify:legal_name:3", "legal_name", "Acme GmbH"), read: read, wantOK: true},
		"free text on a conflict passes":          {selection: selection("clarify:display_name:3", "display_name", "Acme Software Group"), read: read, wantOK: true},
		"stale draft version refused":             {selection: selection("clarify:legal_name:2", "legal_name", "Acme GmbH"), read: read},
		"fabricated clarify refused":              {selection: selection("clarify:icp:3", "icp", "Anyone"), read: read},
		"field swapped under a real id":           {selection: selection("clarify:legal_name:3", "display_name", "Acme GmbH"), read: read},
		"unlisted value on a closed list refused": {selection: selection("clarify:legal_name:3", "legal_name", "Evil Corp"), read: read},
		"no read means no open clarifications":    {selection: selection("clarify:legal_name:3", "legal_name", "Acme GmbH")},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := verifySelectedOption(tc.selection, tc.read, comparisons, "en")
			if tc.wantOK && err != nil {
				t.Fatalf("verifySelectedOption(%+v) = %v, want nil", tc.selection, err)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("verifySelectedOption(%+v) accepted a selection the server never offered", tc.selection)
			}
		})
	}
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
	// The click reaches the model as an explicit administrator statement,
	// so the exact chosen value never depends on the typed prose.
	selectionTurn := brain.request.Messages[len(brain.request.Messages)-2]
	if selectionTurn.Role != chatRoleUser || !strings.Contains(selectionTurn.Content, `"Acme GmbH"`) ||
		!strings.Contains(selectionTurn.Content, "legal_name") {
		t.Fatalf("selection statement missing from model request: %+v", brain.request.Messages)
	}
}

func TestForgedSelectedOptionIsRefusedBeforeTheModelRuns(t *testing.T) {
	readID := ids.NewV7()
	brain := &replyBrainStub{err: errors.New("the model must not run for a forged selection")}
	assistant := onboardingCompanyAssistant{
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7(), SiteReadID: &readID}},
		people: onboardingSiteReadReaderStub{read: people.SiteRead{ID: readID, Status: siteReadWireStatusDone, LegalEntities: []people.SiteReadLegalEntity{
			{Name: "Acme GmbH", SourceURL: "https://acme.example/legal"},
			{Name: "Acme Holding AG", SourceURL: "https://acme.example/legal"},
		}}},
		brain: brain, runtime: &onboardingRuntimeStub{},
	}
	recorder := onboardingCompanyRequest(&assistant, `{
		"message":"Which one is right?","locale":"en",
		"selected_option":{"clarify_id":"clarify:legal_name:0","field":"legal_name","value":"Evil Corp"}}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
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

func TestCompanyActClarificationSkipsQuestionsTheDraftAnswers(t *testing.T) {
	readID := ids.NewV7()
	brain := &validatedOnboardingBrainStub{response: model.Response{Text: `{
		"kind":"clarification","message":"One address question remains.","proposed_changes":[],"source_ids":[]}`}}
	assistant := onboardingCompanyAssistant{
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7(), SiteReadID: &readID}},
		people: onboardingSiteReadReaderStub{read: people.SiteRead{ID: readID, Status: siteReadWireStatusDone, DraftVersion: 3, LegalEntities: []people.SiteReadLegalEntity{
			{Name: "Acme GmbH", RegisteredAddress: "Berlin 1", SourceURL: "https://acme.example/legal"},
			{Name: "Acme Holding AG", RegisteredAddress: "Zug 2", SourceURL: "https://acme.example/legal"},
		}}},
		brain: brain, runtime: &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD"}},
	}
	// The live draft already answers the legal-name question with an
	// exact option value, so the chat re-asks only the address.
	recorder := onboardingCompanyRequest(&assistant, `{
		"message":"What is still unclear?","locale":"en",
		"company_draft":{"legal_name":"Acme GmbH"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.OnboardingCompanyMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.Clarify == nil || reply.Clarify.Field != fieldRegisteredAddress ||
		reply.Clarify.Id != "clarify:registered_address:3" {
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

func TestResultsActRecognizesAManuallySavedCompany(t *testing.T) {
	brain := &validatedOnboardingBrainStub{response: model.Response{Text: `{
		"kind":"answer","message":"Your company profile is in place.","proposed_changes":[],"source_ids":[]}`}}
	assistant := onboardingCompanyAssistant{
		// No site read at all — the company was saved through the manual
		// path, so only the anchor knows it exists.
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}},
		brain: brain, runtime: &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD"}},
		company: onboardingCompanyReaderStub{company: people.Company{DisplayName: "Acme"}},
	}
	recorder := onboardingCompanyRequest(&assistant, `{"message":"Where do I stand?","locale":"en","act":"results"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.OnboardingCompanyMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.AvailableAction == nil || *reply.AvailableAction != crmcontracts.OnboardingAvailableActionFinish ||
		len(reply.RemainingRequiredFields) != 0 {
		t.Fatalf("manual-anchor results reply = %+v", reply)
	}
	if !strings.Contains(brain.request.Messages[0].Content, `"company_confirmed":true`) {
		t.Fatalf("manual-anchor context = %s", brain.request.Messages[0].Content)
	}
}

func TestResultsActWithoutAnyCompanyStaysUnconfirmed(t *testing.T) {
	brain := &validatedOnboardingBrainStub{response: model.Response{Text: `{
		"kind":"answer","message":"The company profile is not saved yet.","proposed_changes":[],"source_ids":[]}`}}
	assistant := onboardingCompanyAssistant{
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}},
		brain: brain, runtime: &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD"}},
		company: onboardingCompanyReaderStub{err: apperrors.ErrNotFound},
	}
	recorder := onboardingCompanyRequest(&assistant, `{"message":"Am I done?","locale":"en","act":"results"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.OnboardingCompanyMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.AvailableAction != nil {
		t.Fatalf("finish must not be offered without a company: %+v", reply)
	}
	if !strings.Contains(brain.request.Messages[0].Content, `"company_confirmed":false`) {
		t.Fatalf("missing-company context = %s", brain.request.Messages[0].Content)
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
