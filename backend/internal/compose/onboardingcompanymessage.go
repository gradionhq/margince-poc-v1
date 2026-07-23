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
	// voice backs the voice act's deterministic context; nil means the
	// role wired no voice store and the act answers without corpus numbers.
	voice onboardingVoiceReader
	// company reports the anchor's presence for the results and connect
	// acts; nil falls back to the site read's confirmation state alone.
	company onboardingCompanyReader
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
	evidence, runID, research, read, comparisons, err := a.onboardingEvidence(r.Context(), state)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	currentDraft := state.CompanyDraft
	if req.CompanyDraft != nil {
		currentDraft = onboardingDraftInput(*req.CompanyDraft)
	}
	remaining := remainingOnboardingFields(currentDraft)
	act := onboardingRequestAct(req)
	if act != string(crmcontracts.OnboardingActCompany) {
		// The recap acts speak about the company that EXISTS, not about
		// the resumable draft: a manually saved anchor is a confirmed
		// company with no required fields left, whatever the draft says.
		present, presenceErr := a.companyPresent(r.Context(), research)
		if presenceErr != nil {
			httperr.Write(w, r, presenceErr)
			return
		}
		if present {
			research.confirmed = true
			remaining = nil
		}
	}
	conversation := onboardingConversationContext{
		Dossier: evidence, CurrentDraft: currentDraft,
		RemainingRequired: remaining,
	}
	if len(remaining) > 0 {
		conversation.NextRequired = remaining[0]
	}

	answer, clarify, actAction, err := a.converse(r.Context(), req, act, message, history, conversation, research, read, comparisons, runID)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	runtime, err := a.runtime.Get(r.Context(), runID)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	response := onboardingCompanyReply(act, answer, evidence, remaining, research, runtime)
	response.Clarify = clarify
	if actAction != nil {
		response.AvailableAction = actAction
	}
	httperr.WriteJSON(w, http.StatusOK, response)
}

// converse routes the message to its act's answer path and returns the
// reply plus the deterministic attachments the act produced: the
// detected clarify question (company act) or the act's next action.
func (a *onboardingCompanyAssistant) converse(ctx context.Context, req crmcontracts.OnboardingCompanyMessageRequest, act, message string, history []model.Message, conversation onboardingConversationContext, research onboardingResearchState, read *people.SiteRead, comparisons []people.SiteReadComparison, runID ids.UUID) (companyReadModelReply, *crmcontracts.OnboardingClarify, *crmcontracts.OnboardingCompanyMessageReplyAvailableAction, error) {
	locale := string(req.Locale)
	remaining := conversation.RemainingRequired
	switch {
	case act != string(crmcontracts.OnboardingActCompany):
		voiceCtx, err := a.voiceContext(ctx)
		if err != nil {
			return companyReadModelReply{}, nil, nil, err
		}
		contextJSON, err := onboardingActContext(act, voiceCtx, a.voice != nil, research, remaining)
		if err != nil {
			return companyReadModelReply{}, nil, nil, err
		}
		answer, err := a.answerAct(principal.WithCorrelationID(ctx, runID), act, message, history, contextJSON, locale)
		if err != nil {
			return companyReadModelReply{}, nil, nil, err
		}
		return answer, nil, onboardingActAction(act, voiceCtx, a.voice != nil, research), nil
	case isCompanyStatusQuestion(message):
		return companyReadModelReply{Kind: companyConversationStatus, Message: onboardingStatusMessage(locale, research, len(remaining))}, nil, nil, nil
	default:
		if req.SelectedOption != nil {
			if err := verifySelectedOption(*req.SelectedOption, read, comparisons, locale); err != nil {
				return companyReadModelReply{}, nil, nil, err
			}
		}
		answer, err := a.answer(principal.WithCorrelationID(ctx, runID), message, history, conversation, locale, req.SelectedOption)
		if err != nil {
			return companyReadModelReply{}, nil, nil, err
		}
		// A clarification carries the first STILL-OPEN server-detected
		// question: the model's prose stays, the options are never its
		// own, and a question the current draft already answers with an
		// exact option value is not re-asked.
		var clarify *crmcontracts.OnboardingClarify
		if answer.Kind == "clarification" && read != nil {
			if questions := openOnboardingClarifies(*read, comparisons, locale, conversation.CurrentDraft); len(questions) > 0 {
				clarify = &questions[0]
			}
		}
		return answer, clarify, nil, nil
	}
}

// onboardingRequestAct resolves the request's act; absent means company.
// Validity was checked at decode time.
func onboardingRequestAct(req crmcontracts.OnboardingCompanyMessageRequest) string {
	if req.Act == nil {
		return string(crmcontracts.OnboardingActCompany)
	}
	return string(*req.Act)
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
	if req.Act != nil && !req.Act.Valid() {
		httperr.Write(w, r, httperr.Validation("act", "invalid", "act must be company, voice, results, or connect"))
		return req, "", false
	}
	if req.SelectedOption != nil {
		if field, code, detail := invalidOnboardingSelection(req); field != "" {
			httperr.Write(w, r, httperr.Validation(field, code, detail))
			return req, "", false
		}
	}
	return req, message, true
}

// invalidOnboardingSelection checks the clarify-option echo: it exists
// only in the company act, and it must name a real company field with a
// non-empty value — the pair it authorizes verbatim.
func invalidOnboardingSelection(req crmcontracts.OnboardingCompanyMessageRequest) (field, code, detail string) {
	selection := *req.SelectedOption
	if onboardingRequestAct(req) != string(crmcontracts.OnboardingActCompany) {
		return "selected_option", "invalid", "a clarify selection applies only to the company act"
	}
	if strings.TrimSpace(selection.ClarifyId) == "" {
		return "selected_option.clarify_id", "empty", "echo the clarify id the option belongs to"
	}
	if !crmcontracts.CompanySiteReadSuggestedChangeField(strings.TrimSpace(selection.Field)).Valid() {
		return "selected_option.field", "invalid", "the selection must name a known company field"
	}
	if strings.TrimSpace(selection.Value) == "" {
		return "selected_option.value", "empty", "the selection must carry the chosen value verbatim"
	}
	return "", "", ""
}

func oversizedOnboardingDraftField(draft crmcontracts.OnboardingCompanyDraft) string {
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

func (a *onboardingCompanyAssistant) onboardingEvidence(ctx context.Context, state identity.OnboardingState) ([]companyReadEvidence, ids.UUID, onboardingResearchState, *people.SiteRead, []people.SiteReadComparison, error) {
	if state.SiteReadID == nil {
		return nil, state.ID, onboardingResearchState{ready: true}, nil, nil, nil
	}
	read, comparisons, err := a.people.GetCompanySiteRead(ctx, *state.SiteReadID)
	if err != nil {
		return nil, ids.UUID{}, onboardingResearchState{}, nil, nil, err
	}
	research := onboardingResearchState{
		status:    read.Status,
		ready:     read.Status == siteReadWireStatusDone || read.Status == siteReadWireStatusPartial,
		confirmed: read.ConfirmedAt != nil,
	}
	return companyReadEvidenceSet(read), read.ID, research, &read, comparisons, nil
}

func (a *onboardingCompanyAssistant) answer(ctx context.Context, message string, history []model.Message, conversation onboardingConversationContext, locale string, selection *crmcontracts.OnboardingClarifySelection) (companyReadModelReply, error) {
	contextJSON, err := json.Marshal(conversation)
	if err != nil {
		return companyReadModelReply{}, err
	}
	messages := make([]model.Message, 0, len(history)+3)
	messages = append(messages, model.Message{Role: chatRoleUser, Content: string(contextJSON)})
	messages = append(messages, history...)
	if selection != nil {
		// The click reaches the model as an explicit administrator
		// statement — without it a bare option label like "Use the
		// website's value" would leave the model guessing which exact
		// value the human chose.
		messages = append(messages, model.Message{Role: chatRoleUser, Content: fmt.Sprintf(
			"I selected %q as the value for %s from your clarification options.",
			strings.TrimSpace(selection.Value), strings.TrimSpace(selection.Field))})
	}
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
	if selection != nil {
		// The clicked option IS an administrator statement: its value is
		// explicitly supplied, and the grant covers exactly that pair.
		statements += " " + selection.Value
		authorization = authorization.withSelectedOption(selection.Field, selection.Value)
	}
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

func onboardingCompanyReply(act string, answer companyReadModelReply, evidence []companyReadEvidence, remaining []string, research onboardingResearchState, runtime ai.RunSummary) crmcontracts.OnboardingCompanyMessageReply {
	base := contractCompanyReadReply(answer, evidence, runtime)
	out := crmcontracts.OnboardingCompanyMessageReply{
		Kind: base.Kind, Message: base.Message, Act: crmcontracts.OnboardingAct(act),
		ProposedChanges: base.ProposedChanges,
		Citations:       base.Citations, RemainingRequiredFields: make([]crmcontracts.OnboardingCompanyMessageReplyRemainingRequiredFields, len(remaining)),
		AiRuntime: base.AiRuntime,
	}
	for i, field := range remaining {
		out.RemainingRequiredFields[i] = crmcontracts.OnboardingCompanyMessageReplyRemainingRequiredFields(field)
	}
	if act != string(crmcontracts.OnboardingActCompany) {
		return out
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
