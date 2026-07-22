// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestCompanyReadReplyAcceptsReviewableChangesAndOnlyKnownSources(t *testing.T) {
	known := map[string]companyReadEvidence{"S1": {ID: "S1", Value: "Acme GmbH", Quote: "Acme GmbH, HRB 12345"}}
	valid := `{"kind":"recommendation","message":"I found the registered name.","proposed_changes":[{"field":"legal_name","value":"Acme GmbH","reason":"The legal notice states it.","source_ids":["S1"]}],"source_ids":["S1"]}`
	if err := validateCompanyReadReply(valid, known, "Which legal name did you find?", true); err != nil {
		t.Fatalf("valid reply rejected: %v", err)
	}

	unknown := `{"kind":"answer","message":"I found it.","proposed_changes":[],"source_ids":["S9"]}`
	if err := validateCompanyReadReply(unknown, known, "Find it", false); err == nil {
		t.Fatal("reply citing a URL outside the dossier was accepted")
	}

	unsupported := `{"kind":"correction","message":"I can change it.","proposed_changes":[{"field":"website","value":"evil.example","reason":"requested","source_ids":[]}],"source_ids":[]}`
	if err := validateCompanyReadReply(unsupported, known, "Use evil.example", true); err == nil {
		t.Fatal("reply proposing a field outside the onboarding vocabulary was accepted")
	}
}

func TestCompanyReadAnswerBuildsABoundedGroundedModelRequest(t *testing.T) {
	brain := &replyBrainStub{response: model.Response{Text: `{
		"kind":"recommendation",
		"message":" I found the legal name. ",
		"proposed_changes":[{"field":"legal_name","value":"Acme GmbH","reason":"The legal notice states it.","source_ids":["S1"]}],
		"source_ids":["S1"]}`}}
	engine := deepReadEngine{brain: brain}
	history := []model.Message{
		{Role: chatRoleUser, Content: "Did you find the imprint?"},
		{Role: "assistant", Content: "Yes, I found one."},
	}

	got, err := engine.answerCompanySiteRead(context.Background(), "Please update the legal name to Acme GmbH.", history, []companyReadEvidence{{
		ID: "S1", Kind: "legal_entity", Field: "legal_identity", Value: "Acme GmbH",
		Quote: "Acme GmbH, HRB 12345", URL: "https://acme.example/imprint",
	}})
	if err != nil {
		t.Fatalf("answerCompanySiteRead: %v", err)
	}
	if got.Message != " I found the legal name. " || len(got.ProposedChanges) != 1 || len(got.SourceIDs) != 1 {
		t.Fatalf("answer = %+v", got)
	}
	if brain.request.System != companyReadMessageSystem || brain.request.MaxTokens != ai.ReasoningOutputMaxTokens ||
		len(brain.request.ResponseSchema) == 0 || brain.request.SecretStripper == nil {
		t.Fatalf("model request lost its governed bounds: %+v", brain.request)
	}
	if len(brain.request.Messages) != 4 || !strings.Contains(brain.request.Messages[0].Content, "Acme GmbH") ||
		brain.request.Messages[1].Content != "Did you find the imprint?" ||
		brain.request.Messages[2].Role != "assistant" || brain.request.Messages[3].Content != "Please update the legal name to Acme GmbH." {
		t.Fatalf("model request lost the administrator or dossier evidence: %+v", brain.request.Messages)
	}

	want := errors.New("provider unavailable")
	engine.brain = &replyBrainStub{err: want}
	if _, err := engine.answerCompanySiteRead(context.Background(), "Try again", nil, nil); !errors.Is(err, want) {
		t.Fatalf("provider error = %v, want %v", err, want)
	}
}

func TestCompanyReadEvidenceIsBoundedNumberedAndWebsiteGrounded(t *testing.T) {
	longValue := strings.Repeat("ü", companyReadSourceMaxRunes+20)
	read := people.SiteRead{
		LegalEntities: []people.SiteReadLegalEntity{{
			Name: "Acme GmbH", RegisteredAddress: "Werkstr. 1", RegisterNumber: "HRB 12345",
			EvidenceSnippet: "Acme GmbH, Werkstr. 1, HRB 12345", SourceURL: "https://acme.example/imprint",
		}},
		ProfileFields: []people.DeepReadField{
			{Field: "offer_summary", Value: longValue, EvidenceSnippet: "Onboarding software", SourceURL: "https://acme.example/product"},
			{Field: "icp", Value: "ignored", SourceURL: ""},
		},
		Facts: []people.DeepReadFact{{
			Field: "service", Value: "Implementation", EvidenceSnippet: "Guided implementation", SourceURL: "https://acme.example/services",
		}},
	}

	got := companyReadEvidenceSet(read)
	if len(got) != 3 || got[0].ID != "S1" || got[1].ID != "S2" || got[2].ID != "S3" {
		t.Fatalf("evidence numbering = %+v", got)
	}
	if got[0].Value != "Acme GmbH · Werkstr. 1 · HRB 12345" || got[0].Kind != "legal_entity" {
		t.Fatalf("legal evidence = %+v", got[0])
	}
	if len([]rune(got[1].Value)) != companyReadSourceMaxRunes || got[2].URL != "https://acme.example/services" {
		t.Fatalf("bounded evidence = %+v", got)
	}
}

func TestCompanyReadReplyMapsChangesCitationsAndExactRuntime(t *testing.T) {
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	got := contractCompanyReadReply(companyReadModelReply{
		Kind:            "recommendation",
		Message:         " I found two grounded details. ",
		ProposedChanges: []companyReadProposedChange{{Field: "legal_name", Value: " Acme GmbH ", Reason: " Imprint ", SourceIDs: []string{"S1"}}},
		SourceIDs:       []string{"S1", "S2"},
	}, []companyReadEvidence{
		{ID: "S1", Kind: "legal_entity", URL: "https://acme.example/imprint"},
		{ID: "S2", Kind: "profile_field", Field: "offer_summary", URL: "https://acme.example/product"},
	}, ai.RunSummary{
		Currency: "USD", CallAttempts: 2, TokensIn: 100, TokensOut: 20, LatencyMS: 300,
		EstimatedCostMicroUSD: 125, UnpricedCalls: 1,
		Models: []ai.RunModelUsage{{
			Task: "cold_start", Tier: "cheap_cloud", Provider: "gemini", ConfiguredModel: "gemini-flash",
			ServedModel: "gemini-flash-2026", CallAttempts: 2, TokensIn: 100, TokensOut: 20,
			CachedTokens: 10, CacheWriteTokens: 5, ReasoningTokens: 3, LatencyMS: 300,
			EstimatedCostMicroUSD: 125, UnpricedCalls: 1, LastUsedAt: now,
		}},
	})

	if got.Message != "I found two grounded details." || len(got.ProposedChanges) != 1 ||
		got.ProposedChanges[0].Value != "Acme GmbH" || len(got.Citations) != 2 {
		t.Fatalf("reply projection = %+v", got)
	}
	if got.Citations[0].Label != "legal_entity" || got.Citations[1].Label != "offer_summary" {
		t.Fatalf("citation labels = %+v", got.Citations)
	}
	if got.AiRuntime.CallAttempts != 2 || got.AiRuntime.EstimatedCostMicrousd != 125 ||
		len(got.AiRuntime.Models) != 1 || got.AiRuntime.Models[0].ServedModel != "gemini-flash-2026" {
		t.Fatalf("runtime projection = %+v", got.AiRuntime)
	}
}

func TestCompanyReadReplyValidationRejectsEveryUnsafeShape(t *testing.T) {
	tooMany := make([]companyReadProposedChange, companyReadChangeLimit+1)
	for i := range tooMany {
		tooMany[i] = companyReadProposedChange{Field: "icp", Value: "A", Reason: "B", SourceIDs: []string{}}
	}
	tests := map[string]companyReadModelReply{
		"empty message":      {Kind: "answer", Message: " "},
		"too many changes":   {Kind: "recommendation", Message: "Answer", ProposedChanges: tooMany},
		"unsupported field":  {Kind: "correction", Message: "Answer", ProposedChanges: []companyReadProposedChange{{Field: "website", Value: "A", Reason: "B", SourceIDs: []string{}}}},
		"empty change value": {Kind: "correction", Message: "Answer", ProposedChanges: []companyReadProposedChange{{Field: "icp", Value: " ", Reason: "B", SourceIDs: []string{}}}},
		"unknown source":     {Kind: "answer", Message: "Answer", SourceIDs: []string{"S9"}},
		"duplicate source":   {Kind: "answer", Message: "Answer", SourceIDs: []string{"S1", "S1"}},
		"fabricated uncited change": {Kind: "correction", Message: "Answer", ProposedChanges: []companyReadProposedChange{{
			Field: "legal_name", Value: "Invented GmbH", Reason: "Claimed", SourceIDs: []string{},
		}}},
		"unrelated cited evidence": {Kind: "recommendation", Message: "Answer", SourceIDs: []string{"S1"}, ProposedChanges: []companyReadProposedChange{{
			Field: "legal_name", Value: "Invented GmbH", Reason: "Claimed", SourceIDs: []string{"S1"},
		}}},
		"hidden change citation": {Kind: "recommendation", Message: "Answer", ProposedChanges: []companyReadProposedChange{{
			Field: "legal_name", Value: "Acme GmbH", Reason: "Claimed", SourceIDs: []string{"S1"},
		}}},
	}
	known := map[string]companyReadEvidence{"S1": {ID: "S1", Value: "Acme GmbH"}}
	for name, reply := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateCompanyReadReplyValue(reply, known, "Use administrator supplied value", true); err == nil {
				t.Fatalf("unsafe reply accepted: %+v", reply)
			}
		})
	}
	if err := validateCompanyReadReply("not json", nil, "", false); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
	adminSupplied := companyReadModelReply{Kind: "correction", Message: "I can suggest that.", ProposedChanges: []companyReadProposedChange{{
		Field: "legal_name", Value: "Admin GmbH", Reason: "You supplied it.", SourceIDs: []string{},
	}}}
	if err := validateCompanyReadReplyValue(adminSupplied, known, "Please use Admin GmbH", true); err != nil {
		t.Fatalf("administrator correction rejected: %v", err)
	}
}

func TestCompanyReadConversationValidatesAndTrimsBoundedHistory(t *testing.T) {
	turns := []crmcontracts.CompanySiteReadConversationTurn{
		{Role: crmcontracts.CompanySiteReadConversationTurnRoleUser, Message: " Earlier question "},
		{Role: crmcontracts.CompanySiteReadConversationTurnRoleAssistant, Message: " Earlier answer "},
	}
	history, err := companyReadConversation(&turns)
	if err != nil || len(history) != 2 || history[0].Content != "Earlier question" || history[1].Role != "assistant" {
		t.Fatalf("history = %+v, err = %v", history, err)
	}
	tooMany := make([]crmcontracts.CompanySiteReadConversationTurn, companyReadHistoryLimit+1)
	if _, err := companyReadConversation(&tooMany); err == nil {
		t.Fatal("oversized conversation history was accepted")
	}
}

func TestCompanyReadMessageHandlerKeepsUnavailableStatesHonest(t *testing.T) {
	readID := openapi_types.UUID(ids.NewV7())
	request := httptest.NewRequest(http.MethodPost, "/v1/company/site-reads/"+readID.String()+"/messages", strings.NewReader(`{"message":"Hello"}`))

	for name, handlers := range map[string]siteReadHandlers{
		"rollout disabled": {companyContextRollout: "off"},
		"engine absent":    {companyContextRollout: "read"},
		"model absent":     {companyContextRollout: "read", engine: &deepReadEngine{}},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handlers.MessageCompanySiteRead(recorder, request.Clone(request.Context()), readID)
			if recorder.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501", recorder.Code)
			}
		})
	}
}

func TestWithDeepReadWiresConversationAndRuntimeFromTheSameOption(t *testing.T) {
	server := Server{}
	brain := &replyBrainStub{}
	WithDeepRead(nil, brain)(&server, nil)

	if server.siteReadHandlers.engine == nil || server.siteReadHandlers.engine.brain != brain ||
		server.siteReadHandlers.engine.runtime == nil || server.assistant == nil ||
		server.assistant.brain != brain {
		t.Fatalf("deep-read workbench wiring = %+v", server.siteReadHandlers)
	}
}

func TestOnboardingCompanyStatusQuestionsNeverBecomeChanges(t *testing.T) {
	for _, question := range []string{"Does this work?", "Is this working?", "Wie ist der Status?", "Funktioniert das?"} {
		if !isCompanyStatusQuestion(question) {
			t.Fatalf("status question %q was not recognized", question)
		}
	}
	if isCompanyStatusQuestion("Change our sales status to active") {
		t.Fatal("an ordinary company correction was classified as a status question")
	}
	if isCompanyStatusQuestion("Does this work with Salesforce?") {
		t.Fatal("an in-scope company question was classified as a workspace status question")
	}

	reply := onboardingCompanyReply(companyReadModelReply{
		Kind: "status", Message: onboardingStatusMessage("en", onboardingResearchState{ready: true}, 2),
	}, nil, []string{"display_name", "icp"}, onboardingResearchState{ready: true}, ai.RunSummary{Currency: "USD"})
	if reply.Kind != crmcontracts.CompanyConversationStatus || len(reply.ProposedChanges) != 0 ||
		reply.NextRequiredField == nil || *reply.NextRequiredField != crmcontracts.OnboardingNextRequiredDisplayName ||
		reply.AvailableAction != nil {
		t.Fatalf("status reply = %+v", reply)
	}
}

func TestCompanyConversationRejectsAChangeHiddenInAStatusReply(t *testing.T) {
	reply := companyReadModelReply{
		Kind: "status", Message: "It works.", ProposedChanges: []companyReadProposedChange{{
			Field: "industry", Value: "Software", Reason: "You said so", SourceIDs: []string{},
		}},
	}
	if err := validateCompanyReadReplyValue(reply, nil, "Software", true); err == nil {
		t.Fatal("status reply carrying a proposed change was accepted")
	}
}

func TestCompanyConversationRejectsSuggestionsWithoutChangeIntent(t *testing.T) {
	reply := companyReadModelReply{
		Kind: "recommendation", Message: "I suggest an update.", ProposedChanges: []companyReadProposedChange{{
			Field: "industry", Value: "Software", Reason: "The dossier says so", SourceIDs: []string{"S1"},
		}}, SourceIDs: []string{"S1"},
	}
	known := map[string]companyReadEvidence{"S1": {ID: "S1", Value: "Software"}}
	if err := validateCompanyReadReplyValue(reply, known, "Does this work?", false); err == nil {
		t.Fatal("an ordinary question produced a change proposal")
	}
}
