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

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestCompanySiteReadMessageUsesTheStoredDossierAndReturnsRuntime(t *testing.T) {
	env := integration.Setup(t)
	read := onboardingDraft(t, env)
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
		reply.Citations[0].Url != seedURL || reply.AiRuntime.Currency != crmcontracts.USD {
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
