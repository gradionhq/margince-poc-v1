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

const onboardingCompanyDraftMaxRunes = 2_000

type onboardingCompanyAssistant struct {
	state   onboardingStateReader
	people  onboardingSiteReadReader
	brain   completer
	runtime runTransparencyReader
	rollout *string
}

type onboardingStateReader interface {
	Get(context.Context) (identity.OnboardingState, error)
}

type onboardingSiteReadReader interface {
	GetCompanySiteRead(context.Context, ids.UUID) (people.SiteRead, []people.SiteReadComparison, error)
}

type onboardingConversationContext struct {
	Dossier           []companyReadEvidence           `json:"dossier_evidence"`
	CurrentDraft      identity.OnboardingCompanyDraft `json:"current_company_draft"`
	NextRequired      string                          `json:"next_required_field,omitempty"`
	RemainingRequired []string                        `json:"remaining_required_fields"`
}

type onboardingResearchState struct {
	status    string
	ready     bool
	confirmed bool
}

func (a *onboardingCompanyAssistant) message(w http.ResponseWriter, r *http.Request) {
	if a == nil || a.brain == nil || a.runtime == nil {
		httperr.NotImplemented(w, r, "messageOnboardingCompany (no model path configured)")
		return
	}
	if a.rollout != nil && !companyContextOnboardingEnabled(*a.rollout) {
		httperr.NotImplemented(w, r, "messageOnboardingCompany (company onboarding disabled)")
		return
	}
	req, message, ok := decodeOnboardingCompanyMessage(w, r)
	if !ok {
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
	evidence, runID, research, err := a.onboardingEvidence(r.Context(), state)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	currentDraft := state.CompanyDraft
	if req.CompanyDraft != nil {
		currentDraft = onboardingConversationDraftInput(*req.CompanyDraft)
	}
	remaining := remainingOnboardingFields(currentDraft)
	conversation := onboardingConversationContext{
		Dossier: evidence, CurrentDraft: currentDraft,
		RemainingRequired: remaining,
	}
	if len(remaining) > 0 {
		conversation.NextRequired = remaining[0]
	}

	var answer companyReadModelReply
	if isCompanyStatusQuestion(message) {
		answer = companyReadModelReply{Kind: companyConversationStatus, Message: onboardingStatusMessage(string(req.Locale), research, len(remaining))}
	} else {
		callCtx := principal.WithCorrelationID(r.Context(), runID)
		answer, err = a.answer(callCtx, message, history, conversation, string(req.Locale))
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
	response := onboardingCompanyReply(answer, evidence, remaining, research, runtime)
	httperr.WriteJSON(w, http.StatusOK, response)
}

func decodeOnboardingCompanyMessage(w http.ResponseWriter, r *http.Request) (crmcontracts.OnboardingCompanyMessageRequest, string, bool) {
	var req crmcontracts.OnboardingCompanyMessageRequest
	if !httperr.Decode(w, r, &req) {
		return req, "", false
	}
	if !req.Locale.Valid() {
		httperr.Write(w, r, httperr.Validation("locale", "invalid", "locale must be en or de"))
		return req, "", false
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		httperr.Write(w, r, httperr.Validation("message", "empty", "write a company-setup message for Margince"))
		return req, "", false
	}
	if len([]rune(message)) > companyReadMessageMaxRunes {
		httperr.Write(w, r, httperr.Validation("message", "too_long", "message must be at most 2000 characters"))
		return req, "", false
	}
	if req.CompanyDraft != nil {
		if field := oversizedOnboardingDraftField(*req.CompanyDraft); field != "" {
			httperr.Write(w, r, httperr.Validation("company_draft."+field, "too_long", "each company draft field must be at most 2000 characters"))
			return req, "", false
		}
	}
	return req, message, true
}

func onboardingConversationDraftInput(in crmcontracts.OnboardingCompanyConversationDraft) identity.OnboardingCompanyDraft {
	return identity.OnboardingCompanyDraft{
		DisplayName:       in.DisplayName,
		OfferSummary:      in.OfferSummary,
		ICP:               in.Icp,
		ValueProposition:  in.ValueProposition,
		USP:               in.Usp,
		CustomerPains:     in.CustomerPains,
		DesiredOutcomes:   in.DesiredOutcomes,
		BuyingCenter:      in.BuyingCenter,
		BuyingIntents:     in.BuyingIntents,
		CommonObjections:  in.CommonObjections,
		SalesMotion:       in.SalesMotion,
		LegalName:         in.LegalName,
		RegisteredAddress: in.RegisteredAddress,
		RegisterVAT:       in.RegisterVat,
		Industry:          in.Industry,
		History:           in.History,
	}
}

func oversizedOnboardingDraftField(draft crmcontracts.OnboardingCompanyConversationDraft) string {
	fields := []struct {
		name  string
		value *string
	}{
		{fieldDisplayName, draft.DisplayName},
		{fieldOfferSummary, draft.OfferSummary},
		{fieldICP, draft.Icp},
		{fieldValueProposition, draft.ValueProposition},
		{fieldUSP, draft.Usp},
		{fieldCustomerPains, draft.CustomerPains},
		{fieldDesiredOutcomes, draft.DesiredOutcomes},
		{fieldBuyingCenter, draft.BuyingCenter},
		{fieldBuyingIntents, draft.BuyingIntents},
		{fieldCommonObjections, draft.CommonObjections},
		{fieldSalesMotion, draft.SalesMotion},
		{fieldLegalName, draft.LegalName},
		{fieldRegisteredAddress, draft.RegisteredAddress},
		{fieldRegisterVat, draft.RegisterVat},
		{fieldIndustry, draft.Industry},
		{fieldHistory, draft.History},
	}
	for _, field := range fields {
		if field.value != nil && len([]rune(*field.value)) > onboardingCompanyDraftMaxRunes {
			return field.name
		}
	}
	return ""
}

func (a *onboardingCompanyAssistant) onboardingEvidence(ctx context.Context, state identity.OnboardingState) ([]companyReadEvidence, ids.UUID, onboardingResearchState, error) {
	if state.SiteReadID == nil {
		return nil, state.ID, onboardingResearchState{ready: true}, nil
	}
	read, _, err := a.people.GetCompanySiteRead(ctx, *state.SiteReadID)
	if err != nil {
		return nil, ids.UUID{}, onboardingResearchState{}, err
	}
	research := onboardingResearchState{
		status:    read.Status,
		ready:     read.Status == siteReadWireStatusDone || read.Status == siteReadWireStatusPartial,
		confirmed: read.ConfirmedAt != nil,
	}
	return companyReadEvidenceSet(read), read.ID, research, nil
}

func (a *onboardingCompanyAssistant) answer(ctx context.Context, message string, history []model.Message, conversation onboardingConversationContext, locale string) (companyReadModelReply, error) {
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
The current_company_draft is application state, not an administrator statement. remaining_required_fields is the deterministic completion plan. If the administrator directly answers next_required_field, classify the response as correction and propose that exact value for that field. After answering an in-scope question, briefly return to the next required field.
Respond in ` + locale + `.`,
		Messages: messages, MaxTokens: ai.ReasoningOutputMaxTokens,
		ResponseSchema: companyReadMessageSchema, SecretStripper: ai.NewSecretStripper(),
	}
	known := make(map[string]companyReadEvidence, len(conversation.Dossier))
	for _, source := range conversation.Dossier {
		known[source.ID] = source
	}
	statements := administratorConversation(history, message)
	authorization := newCompanyChangeAuthorization(message, history, conversation.NextRequired)
	validate := func(text string) error { return validateCompanyReadReply(text, known, statements, authorization) }
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
	if err := validateCompanyReadReplyValue(reply, known, statements, authorization); err != nil {
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
	normalized = strings.TrimRight(normalized, "?!. ")
	for _, phrase := range []string{
		"does this work", "does this work now", "is this working", "is this working now", "is this working yet",
		"what is the status", "what is the status now", "what is the status of the website research", "what is the status of website research",
		"funktioniert das", "funktioniert das jetzt", "klappt das", "klappt das jetzt", "wie ist der status", "wie ist jetzt der status",
		"wie ist der status der web-recherche", "wie ist der status der website-recherche",
	} {
		if normalized == phrase {
			return true
		}
	}
	return false
}

func onboardingStatusMessage(locale string, research onboardingResearchState, missing int) string {
	if locale == "de" {
		if research.confirmed {
			return "Ja. Ich habe das bestätigte Unternehmensprofil gespeichert."
		}
		if research.status == siteReadWireStatusFailed {
			return fmt.Sprintf("Meine Web-Recherche ist beendet, konnte aber nicht abgeschlossen werden. Wir können die %d fehlenden Pflichtangaben hier gemeinsam manuell ergänzen; gespeichert ist noch nichts.", missing)
		}
		if !research.ready {
			return "Ja. Ich recherchiere noch und zeige neue belegte Funde im Unternehmensentwurf. Gespeichert ist noch nichts."
		}
		return fmt.Sprintf("Ja. Meine Recherche funktioniert. Es fehlen noch %d Pflichtangaben; gespeichert ist noch nichts.", missing)
	}
	if research.confirmed {
		return "Yes. I saved the confirmed company profile."
	}
	if research.status == siteReadWireStatusFailed {
		return fmt.Sprintf("My website research has stopped without completing. We can fill the %d missing required details together here; nothing is saved yet.", missing)
	}
	if !research.ready {
		return "Yes. I'm still researching and will add grounded findings to the company draft as they arrive. Nothing is saved yet."
	}
	return fmt.Sprintf("Yes. The company workspace is working. %d required details remain; nothing is saved yet.", missing)
}

func onboardingCompanyReply(answer companyReadModelReply, evidence []companyReadEvidence, remaining []string, research onboardingResearchState, runtime ai.RunSummary) crmcontracts.OnboardingCompanyMessageReply {
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
	} else if research.ready && !research.confirmed {
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
