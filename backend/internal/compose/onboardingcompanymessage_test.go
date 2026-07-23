// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

type onboardingStateReaderStub struct {
	state identity.OnboardingState
	err   error
}

func (s onboardingStateReaderStub) Get(context.Context) (identity.OnboardingState, error) {
	return s.state, s.err
}

type onboardingSiteReadReaderStub struct {
	read        people.SiteRead
	comparisons []people.SiteReadComparison
	err         error
}

func (s onboardingSiteReadReaderStub) GetCompanySiteRead(context.Context, ids.UUID) (people.SiteRead, []people.SiteReadComparison, error) {
	return s.read, s.comparisons, s.err
}

type onboardingRuntimeStub struct {
	summary ai.RunSummary
	err     error
	runID   ids.UUID
}

func (s *onboardingRuntimeStub) Get(_ context.Context, runID ids.UUID) (ai.RunSummary, error) {
	s.runID = runID
	return s.summary, s.err
}

type validatedOnboardingBrainStub struct {
	response model.Response
	request  model.Request
}

func (b *validatedOnboardingBrainStub) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("unvalidated completion was used")
}

func (b *validatedOnboardingBrainStub) CompleteValidated(_ context.Context, req model.Request, validate ai.Validator) (model.Response, error) {
	b.request = req
	if err := validate(b.response.Text); err != nil {
		return model.Response{}, err
	}
	return b.response, nil
}

func stringPtr(value string) *string { return &value }

func TestOnboardingCompanyMessageAnswersAndReturnsTheDeterministicNextField(t *testing.T) {
	stateID := ids.NewV7()
	brain := &validatedOnboardingBrainStub{response: model.Response{Text: `{
		"kind":"correction",
		"message":"I'll use Acme as the company name.",
		"proposed_changes":[{"field":"display_name","value":"Acme","reason":"You supplied it.","source_ids":[]}],
		"source_ids":[]}`}}
	runtime := &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD", CallAttempts: 1}}
	assistant := onboardingCompanyAssistant{
		state: onboardingStateReaderStub{state: identity.OnboardingState{
			ID: stateID,
			CompanyDraft: identity.OnboardingCompanyDraft{
				OfferSummary: stringPtr("CRM software"), ICP: stringPtr("Revenue teams"),
			},
		}},
		people: onboardingSiteReadReaderStub{}, brain: brain, runtime: runtime,
	}

	recorder := onboardingCompanyRequest(&assistant, `{
		"message":"Use Acme as our company name",
		"locale":"en",
		"history":[{"role":"user","message":"Let us finish setup"},{"role":"assistant","message":"What is the company name?"}]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.OnboardingCompanyMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.Kind != crmcontracts.CompanyConversationCorrection || len(reply.ProposedChanges) != 1 ||
		reply.ProposedChanges[0].Field != crmcontracts.DisplayName ||
		reply.NextRequiredField == nil || *reply.NextRequiredField != crmcontracts.OnboardingNextRequiredDisplayName ||
		reply.AvailableAction != nil || runtime.runID != stateID {
		t.Fatalf("reply = %+v, runtime run = %s", reply, runtime.runID)
	}
	if len(brain.request.Messages) != 4 || !strings.Contains(brain.request.Messages[0].Content, `"next_required_field":"display_name"`) ||
		brain.request.Messages[3].Content != "Use Acme as our company name" || brain.request.SecretStripper == nil {
		t.Fatalf("model request = %+v", brain.request)
	}
}

func TestOnboardingCompanyStatusReportsLiveResearchWithoutCallingTheModel(t *testing.T) {
	stateID, readID := ids.NewV7(), ids.NewV7()
	brain := &replyBrainStub{err: errors.New("model must not be called for status")}
	runtime := &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD"}}
	assistant := onboardingCompanyAssistant{
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: stateID, SiteReadID: &readID}},
		people: onboardingSiteReadReaderStub{read: people.SiteRead{
			ID: readID, Status: "running", ProfileFields: []people.DeepReadField{{
				Field: "offer_summary", Value: "CRM software", EvidenceSnippet: "CRM software", SourceURL: "https://acme.example",
			}},
		}},
		brain: brain, runtime: runtime,
	}

	recorder := onboardingCompanyRequest(&assistant, `{"message":"Does this work?","locale":"en"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.OnboardingCompanyMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.Kind != crmcontracts.CompanyConversationStatus || !strings.Contains(reply.Message, "still researching") ||
		len(reply.ProposedChanges) != 0 || len(reply.Citations) != 0 || runtime.runID != readID || brain.request.System != "" {
		t.Fatalf("status reply = %+v, runtime run = %s, model request = %+v", reply, runtime.runID, brain.request)
	}
}

func TestOnboardingCompanyStatusOffersConfirmationOnlyWhenComplete(t *testing.T) {
	readID := ids.NewV7()
	complete := identity.OnboardingCompanyDraft{
		DisplayName: stringPtr("Acme"), OfferSummary: stringPtr("CRM software"), ICP: stringPtr("Revenue teams"),
	}
	assistant := onboardingCompanyAssistant{
		state:  onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7(), SiteReadID: &readID, CompanyDraft: complete}},
		people: onboardingSiteReadReaderStub{read: people.SiteRead{ID: readID, Status: siteReadWireStatusDone}},
		brain:  &replyBrainStub{}, runtime: &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD"}},
	}

	recorder := onboardingCompanyRequest(&assistant, `{"message":"Wie ist der Status?","locale":"de"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.OnboardingCompanyMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.AvailableAction == nil || *reply.AvailableAction != crmcontracts.OnboardingAvailableActionConfirmCompany ||
		reply.NextRequiredField != nil || len(reply.RemainingRequiredFields) != 0 || !strings.Contains(reply.Message, "0 Pflichtangaben") {
		t.Fatalf("complete status reply = %+v", reply)
	}
}

func TestOnboardingCompanyMessageRejectsInvalidRequestsAtTheirBoundary(t *testing.T) {
	validState := onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}}
	tests := map[string]struct {
		assistant onboardingCompanyAssistant
		body      string
	}{
		"malformed body":    {assistant: onboardingCompanyAssistant{state: validState, brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{}}, body: "{"},
		"empty message":     {assistant: onboardingCompanyAssistant{state: validState, brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{}}, body: `{"message":"  ","locale":"en"}`},
		"oversized message": {assistant: onboardingCompanyAssistant{state: validState, brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{}}, body: `{"message":"` + strings.Repeat("x", companyReadMessageMaxRunes+1) + `","locale":"en"}`},
		"oversized draft":   {assistant: onboardingCompanyAssistant{state: validState, brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{}}, body: `{"message":"Hello","locale":"en","company_draft":{"icp":"` + strings.Repeat("x", onboardingCompanyDraftMaxRunes+1) + `"}}`},
		"invalid history":   {assistant: onboardingCompanyAssistant{state: validState, brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{}}, body: `{"message":"Hello","locale":"en","history":[{"role":"user","message":""}]}`},
		"missing locale":    {assistant: onboardingCompanyAssistant{state: validState, brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{}}, body: `{"message":"Hello"}`},
		"unknown locale":    {assistant: onboardingCompanyAssistant{state: validState, brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{}}, body: `{"message":"Hello","locale":"fr"}`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := onboardingCompanyRequest(&tc.assistant, tc.body)
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestOnboardingCompanyMessageUsesTheLiveDraftAndRequestedLanguage(t *testing.T) {
	brain := &validatedOnboardingBrainStub{response: model.Response{Text: `{
		"kind":"answer","message":"Das passt.","proposed_changes":[],"source_ids":[]}`}}
	assistant := onboardingCompanyAssistant{
		state:  onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}},
		people: onboardingSiteReadReaderStub{}, brain: brain,
		runtime: &onboardingRuntimeStub{summary: ai.RunSummary{Currency: "USD"}},
	}
	recorder := onboardingCompanyRequest(&assistant, `{
		"message":"Passt das für den Mittelstand?","locale":"de",
		"company_draft":{"display_name":"Acme","offer_summary":"CRM","icp":"Mittelstand"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.OnboardingCompanyMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.RemainingRequiredFields) != 0 || reply.AvailableAction == nil {
		t.Fatalf("live draft was not used: %+v", reply)
	}
	if !strings.Contains(brain.request.System, "Respond in de.") ||
		!strings.Contains(brain.request.Messages[0].Content, `"display_name":"Acme"`) {
		t.Fatalf("locale or live draft missing from model request: %+v", brain.request)
	}
}

func TestOnboardingCompanyMessageHonorsTheRequestTimeRollout(t *testing.T) {
	rollout := companyContextRolloutOff
	assistant := onboardingCompanyAssistant{
		state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}},
		brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{}, rollout: &rollout,
	}
	recorder := onboardingCompanyRequest(&assistant, `{"message":"Hello","locale":"en"}`)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestOnboardingCompanyMessageReturnsDependencyFailures(t *testing.T) {
	want := errors.New("dependency unavailable")
	readID := ids.NewV7()
	tests := map[string]onboardingCompanyAssistant{
		"state": {
			state: onboardingStateReaderStub{err: want}, brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{},
		},
		"site read": {
			state:  onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7(), SiteReadID: &readID}},
			people: onboardingSiteReadReaderStub{err: want}, brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{},
		},
		"model": {
			state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}}, people: onboardingSiteReadReaderStub{},
			brain: &replyBrainStub{err: want}, runtime: &onboardingRuntimeStub{},
		},
		"runtime": {
			state: onboardingStateReaderStub{state: identity.OnboardingState{ID: ids.NewV7()}}, people: onboardingSiteReadReaderStub{},
			brain: &replyBrainStub{}, runtime: &onboardingRuntimeStub{err: want},
		},
	}
	for name, assistant := range tests {
		t.Run(name, func(t *testing.T) {
			body := `{"message":"Tell me about this company","locale":"en"}`
			if name == "runtime" {
				body = `{"message":"Does this work?","locale":"en"}`
			}
			recorder := onboardingCompanyRequest(&assistant, body)
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestOnboardingCompanyMessageRequiresAConfiguredAssistant(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/onboarding/company/messages", strings.NewReader(`{"message":"Hello","locale":"en"}`))
	recorder := httptest.NewRecorder()
	onboardingStateHandlers{}.MessageOnboardingCompany(recorder, request)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("nil handler status = %d", recorder.Code)
	}

	recorder = onboardingCompanyRequest(&onboardingCompanyAssistant{}, `{"message":"Hello","locale":"en"}`)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("unwired assistant status = %d", recorder.Code)
	}
}

func TestOnboardingStatusMessagesCoverBothLocalesAndResearchStates(t *testing.T) {
	for _, tc := range []struct {
		locale    string
		readReady bool
		want      string
	}{
		{locale: "en", readReady: false, want: "still researching"},
		{locale: "en", readReady: true, want: "2 required details"},
		{locale: "de", readReady: false, want: "recherchiere noch"},
		{locale: "de", readReady: true, want: "2 Pflichtangaben"},
	} {
		if got := onboardingStatusMessage(tc.locale, onboardingResearchState{ready: tc.readReady}, 2); !strings.Contains(got, tc.want) {
			t.Fatalf("onboardingStatusMessage(%q, %v) = %q", tc.locale, tc.readReady, got)
		}
	}
	if got := onboardingStatusMessage("en", onboardingResearchState{status: siteReadWireStatusFailed}, 2); !strings.Contains(got, "stopped") {
		t.Fatalf("failed research status = %q", got)
	}
	if got := onboardingStatusMessage("en", onboardingResearchState{confirmed: true}, 0); !strings.Contains(got, "saved") {
		t.Fatalf("confirmed research status = %q", got)
	}
}

func onboardingCompanyRequest(assistant *onboardingCompanyAssistant, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/onboarding/company/messages", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	onboardingStateHandlers{assistant: assistant}.MessageOnboardingCompany(recorder, request)
	return recorder
}
