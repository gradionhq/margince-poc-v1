// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestCompanySiteReadMessageUsesTheStoredDossierAndReturnsRuntime(t *testing.T) {
	env := integration.Setup(t)
	read := onboardingDraft(t, env)
	usedAt := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	env.WsExec(t, `
		INSERT INTO ai_model_rate (workspace_id, provider, model_id, input_per_mtok_microusd,
		  output_per_mtok_microusd, cache_read_per_mtok_microusd, cache_write_per_mtok_microusd, effective_date)
		VALUES ($1,'anthropic','claude-workbench-test',1000000,5000000,500000,2000000,$2)`, env.WS, usedAt)
	env.WsExec(t, `
		INSERT INTO ai_call (workspace_id, correlation_id, task, tier, provider, model_id,
		  served_model, request_fingerprint, tokens_in, tokens_out, reasoning_tokens,
		  cached_tokens, cache_write_tokens, latency_ms, occurred_at, logical_call_id)
		VALUES ($1,$2,$3,$4,'anthropic','claude-workbench-test','claude-workbench-test-202607',$5,
		  1000,100,20,200,50,750,$6,$7)`, env.WS, read.ID, string(ai.TaskColdStart),
		string(ai.TierCheapCloud), "workbench-fingerprint", usedAt, read.ID)
	human := env.As(env.Rep1, nil, integration.AdminPerms)
	brain := &replyBrainStub{response: model.Response{Text: `{
		"message":"I found the company name on the home page.",
		"proposed_changes":[{"field":"display_name","value":"Acme","reason":"The page states it."}],
		"source_ids":["S1"]}`}}
	engine := &deepReadEngine{people: env.People, brain: brain, runtime: ai.NewRunTransparency(env.Pool)}

	request := companyReadMessageRequest(human, t, read.ID.String(), "Which name did you find?")
	recorder := httptest.NewRecorder()
	engine.messageCompanySiteRead(recorder, request, openapi_types.UUID(read.ID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("message → %d %s", recorder.Code, recorder.Body.String())
	}
	var reply crmcontracts.CompanySiteReadMessageReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Message == "" || len(reply.ProposedChanges) != 1 || len(reply.Citations) != 1 ||
		reply.Citations[0].Url != seedURL || reply.AiRuntime.Currency != crmcontracts.USD ||
		reply.AiRuntime.CallAttempts != 1 || reply.AiRuntime.TokensIn != 1_000 ||
		reply.AiRuntime.TokensOut != 100 || reply.AiRuntime.EstimatedCostMicrousd != 1_450 ||
		len(reply.AiRuntime.Models) != 1 || reply.AiRuntime.Models[0].ServedModel != "claude-workbench-test-202607" {
		t.Fatalf("message reply = %+v", reply)
	}
	if !strings.Contains(brain.request.Messages[0].Content, "Which name did you find?") {
		t.Fatalf("administrator message did not reach the model data frame: %+v", brain.request.Messages)
	}

	for name, message := range map[string]string{
		"empty":    "   ",
		"too long": strings.Repeat("x", companyReadMessageMaxRunes+1),
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.messageCompanySiteRead(recorder, companyReadMessageRequest(human, t, read.ID.String(), message), openapi_types.UUID(read.ID))
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func companyReadMessageRequest(ctx context.Context, t *testing.T, readID, message string) *http.Request {
	t.Helper()
	body, err := json.Marshal(crmcontracts.CompanySiteReadMessageRequest{Message: message})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, "/v1/company/site-reads/"+readID+"/messages", strings.NewReader(string(body))).WithContext(ctx)
}
