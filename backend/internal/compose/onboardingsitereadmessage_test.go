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

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestCompanyReadReplyAcceptsReviewableChangesAndOnlyKnownSources(t *testing.T) {
	known := map[string]struct{}{"S1": {}}
	valid := `{"message":"I found the registered name.","proposed_changes":[{"field":"legal_name","value":"Acme GmbH","reason":"The legal notice states it."}],"source_ids":["S1"]}`
	if err := validateCompanyReadReply(valid, known); err != nil {
		t.Fatalf("valid reply rejected: %v", err)
	}

	unknown := `{"message":"I found it.","proposed_changes":[],"source_ids":["S9"]}`
	if err := validateCompanyReadReply(unknown, known); err == nil {
		t.Fatal("reply citing a URL outside the dossier was accepted")
	}

	unsupported := `{"message":"I can change it.","proposed_changes":[{"field":"website","value":"evil.example","reason":"requested"}],"source_ids":[]}`
	if err := validateCompanyReadReply(unsupported, known); err == nil {
		t.Fatal("reply proposing a field outside the onboarding vocabulary was accepted")
	}
}

func TestCompanyReadAnswerBuildsABoundedGroundedModelRequest(t *testing.T) {
	brain := &replyBrainStub{response: model.Response{Text: `{
		"message":" I found the legal name. ",
		"proposed_changes":[{"field":"legal_name","value":"Acme GmbH","reason":"The legal notice states it."}],
		"source_ids":["S1"]}`}}
	engine := deepReadEngine{brain: brain}

	got, err := engine.answerCompanySiteRead(context.Background(), "Which legal name did you find?", []companyReadEvidence{{
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
	if len(brain.request.Messages) != 1 || !strings.Contains(brain.request.Messages[0].Content, "Which legal name") ||
		!strings.Contains(brain.request.Messages[0].Content, "Acme GmbH") {
		t.Fatalf("model request lost the administrator or dossier evidence: %+v", brain.request.Messages)
	}

	want := errors.New("provider unavailable")
	engine.brain = &replyBrainStub{err: want}
	if _, err := engine.answerCompanySiteRead(context.Background(), "Try again", nil); !errors.Is(err, want) {
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
		Message:         " I found two grounded details. ",
		ProposedChanges: []companyReadProposedChange{{Field: "legal_name", Value: " Acme GmbH ", Reason: " Imprint "}},
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
		tooMany[i] = companyReadProposedChange{Field: "icp", Value: "A", Reason: "B"}
	}
	tests := map[string]companyReadModelReply{
		"empty message":      {Message: " "},
		"too many changes":   {Message: "Answer", ProposedChanges: tooMany},
		"unsupported field":  {Message: "Answer", ProposedChanges: []companyReadProposedChange{{Field: "website", Value: "A", Reason: "B"}}},
		"empty change value": {Message: "Answer", ProposedChanges: []companyReadProposedChange{{Field: "icp", Value: " ", Reason: "B"}}},
		"unknown source":     {Message: "Answer", SourceIDs: []string{"S9"}},
		"duplicate source":   {Message: "Answer", SourceIDs: []string{"S1", "S1"}},
	}
	for name, reply := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateCompanyReadReplyValue(reply, map[string]struct{}{"S1": {}}); err == nil {
				t.Fatalf("unsafe reply accepted: %+v", reply)
			}
		})
	}
	if err := validateCompanyReadReply("not json", nil); err == nil {
		t.Fatal("malformed JSON was accepted")
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
		server.siteReadHandlers.engine.runtime == nil {
		t.Fatalf("deep-read workbench wiring = %+v", server.siteReadHandlers)
	}
}
