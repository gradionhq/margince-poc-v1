// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

var onboardingRequiredFields = []string{fieldDisplayName, fieldOfferSummary, fieldICP}

type onboardingCompanyAssistant struct {
	state   *identity.OnboardingStore
	people  *people.Store
	brain   completer
	runtime runTransparencyReader
}

type onboardingConversationContext struct {
	Dossier           []companyReadEvidence           `json:"dossier_evidence"`
	CurrentDraft      identity.OnboardingCompanyDraft `json:"current_company_draft"`
	NextRequired      string                          `json:"next_required_field,omitempty"`
	RemainingRequired []string                        `json:"remaining_required_fields"`
}

func (a *onboardingCompanyAssistant) message(w http.ResponseWriter, r *http.Request) {
	if a == nil || a.brain == nil || a.runtime == nil {
		httperr.NotImplemented(w, r, "messageOnboardingCompany (no model path configured)")
		return
	}
	var req crmcontracts.OnboardingCompanyMessageRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		httperr.Write(w, r, httperr.Validation("message", "empty", "write a company-setup message for Margince"))
		return
	}
	state, err := a.state.Get(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	history, validationErr := companyReadConversation(req.History)
	if validationErr != nil {
		httperr.Write(w, r, httperr.Validation("history", "invalid", validationErr.Error()))
		return
	}
	evidence, runID, readReady, err := a.onboardingEvidence(r.Context(), state)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	remaining := remainingOnboardingFields(state.CompanyDraft)
	conversation := onboardingConversationContext{
		Dossier: evidence, CurrentDraft: state.CompanyDraft,
		RemainingRequired: remaining,
	}
	if len(remaining) > 0 {
		conversation.NextRequired = remaining[0]
	}

	var answer companyReadModelReply
	if isCompanyStatusQuestion(message) {
		answer = companyReadModelReply{Kind: companyConversationStatus, Message: onboardingStatusMessage(string(req.Locale), readReady, len(remaining))}
	} else {
		callCtx := principal.WithCorrelationID(r.Context(), runID)
		answer, err = a.answer(callCtx, message, history, conversation)
		if err != nil {
			httperr.Write(w, r, err)
			return
		}
	}
	runtime, err := a.runtime.Get(r.Context(), runID)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	response := onboardingCompanyReply(answer, evidence, remaining, readReady, runtime)
	httperr.WriteJSON(w, http.StatusOK, response)
}

func (a *onboardingCompanyAssistant) onboardingEvidence(ctx context.Context, state identity.OnboardingState) ([]companyReadEvidence, ids.UUID, bool, error) {
	if state.SiteReadID == nil {
		return nil, state.ID, true, nil
	}
	read, _, err := a.people.GetCompanySiteRead(ctx, *state.SiteReadID)
	if err != nil {
		return nil, ids.UUID{}, false, err
	}
	ready := read.Status == siteReadWireStatusDone || read.Status == siteReadWireStatusPartial || read.ConfirmedAt != nil
	return companyReadEvidenceSet(read), read.ID, ready, nil
}

func (a *onboardingCompanyAssistant) answer(ctx context.Context, message string, history []model.Message, conversation onboardingConversationContext) (companyReadModelReply, error) {
	contextJSON, err := json.Marshal(conversation)
	if err != nil {
		return companyReadModelReply{}, err
	}
	messages := make([]model.Message, 0, len(history)+2)
	messages = append(messages, model.Message{Role: chatRoleUser, Content: string(contextJSON)})
	messages = append(messages, history...)
	messages = append(messages, model.Message{Role: chatRoleUser, Content: message})
	req := model.Request{
		System: companyReadMessageSystem + `
The current_company_draft is application state, not an administrator statement. remaining_required_fields is the deterministic completion plan. If the administrator directly answers next_required_field, classify the response as correction and propose that exact value for that field. After answering an in-scope question, briefly return to the next required field.`,
		Messages: messages, MaxTokens: ai.ReasoningOutputMaxTokens,
		ResponseSchema: companyReadMessageSchema, SecretStripper: ai.NewSecretStripper(),
	}
	known := make(map[string]companyReadEvidence, len(conversation.Dossier))
	for _, source := range conversation.Dossier {
		known[source.ID] = source
	}
	statements := administratorConversation(history, message)
	validate := func(text string) error { return validateCompanyReadReply(text, known, statements) }
	var response model.Response
	if structured, ok := a.brain.(validatedBrain); ok {
		response, err = structured.CompleteValidated(ctx, req, validate)
	} else {
		response, err = a.brain.Complete(ctx, req)
	}
	if err != nil {
		return companyReadModelReply{}, err
	}
	var reply companyReadModelReply
	if err := json.Unmarshal([]byte(ai.Unfence(response.Text)), &reply); err != nil {
		return companyReadModelReply{}, fmt.Errorf("compose: onboarding company answer is not valid JSON: %w", err)
	}
	if err := validateCompanyReadReplyValue(reply, known, statements); err != nil {
		return companyReadModelReply{}, err
	}
	return reply, nil
}

func remainingOnboardingFields(draft identity.OnboardingCompanyDraft) []string {
	values := map[string]*string{
		fieldDisplayName:  draft.DisplayName,
		fieldOfferSummary: draft.OfferSummary,
		fieldICP:          draft.ICP,
	}
	remaining := make([]string, 0, len(onboardingRequiredFields))
	for _, field := range onboardingRequiredFields {
		value := values[field]
		if value == nil || strings.TrimSpace(*value) == "" {
			remaining = append(remaining, field)
		}
	}
	return remaining
}

func isCompanyStatusQuestion(message string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(message), " "))
	for _, phrase := range []string{"does this work", "is this working", "what is the status", "funktioniert das", "klappt das", "wie ist der status"} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func onboardingStatusMessage(locale string, readReady bool, missing int) string {
	if locale == "de" {
		if !readReady {
			return "Ja. Ich recherchiere noch und zeige neue belegte Funde im Unternehmensentwurf. Gespeichert ist noch nichts."
		}
		return fmt.Sprintf("Ja. Meine Recherche funktioniert. Es fehlen noch %d Pflichtangaben; gespeichert ist noch nichts.", missing)
	}
	if !readReady {
		return "Yes. I'm still researching and will add grounded findings to the company draft as they arrive. Nothing is saved yet."
	}
	return fmt.Sprintf("Yes. The company workspace is working. %d required details remain; nothing is saved yet.", missing)
}

func onboardingCompanyReply(answer companyReadModelReply, evidence []companyReadEvidence, remaining []string, readReady bool, runtime ai.RunSummary) crmcontracts.OnboardingCompanyMessageReply {
	base := contractCompanyReadReply(answer, evidence, runtime)
	out := crmcontracts.OnboardingCompanyMessageReply{
		Kind: base.Kind, Message: base.Message, ProposedChanges: base.ProposedChanges,
		Citations: base.Citations, RemainingRequiredFields: make([]crmcontracts.OnboardingCompanyMessageReplyRemainingRequiredFields, len(remaining)),
		AiRuntime: base.AiRuntime,
	}
	for i, field := range remaining {
		out.RemainingRequiredFields[i] = crmcontracts.OnboardingCompanyMessageReplyRemainingRequiredFields(field)
	}
	if len(remaining) > 0 {
		next := crmcontracts.OnboardingCompanyMessageReplyNextRequiredField(remaining[0])
		out.NextRequiredField = &next
	} else if readReady {
		action := crmcontracts.OnboardingAvailableActionConfirmCompany
		out.AvailableAction = &action
	}
	return out
}

func (h onboardingStateHandlers) MessageOnboardingCompany(w http.ResponseWriter, r *http.Request) {
	if h.assistant == nil {
		httperr.NotImplemented(w, r, "messageOnboardingCompany (no model path configured)")
		return
	}
	h.assistant.message(w, r)
}
