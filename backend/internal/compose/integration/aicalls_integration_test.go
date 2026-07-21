// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type seededAiCalls struct {
	newest ids.UUID
	older  ids.UUID
}

func seedAiCallTrace(t *testing.T, e *env) seededAiCalls {
	t.Helper()
	var workspaceID ids.UUID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&workspaceID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	newest, retry, older := ids.NewV7(), ids.NewV7(), ids.NewV7()
	logical := ids.NewV7()
	base := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	insert := `INSERT INTO ai_call (
		id, workspace_id, task, tier, provider, model_id, request_fingerprint,
		tokens_in, tokens_out, latency_ms, error_sentinel, logical_call_id,
		attempt, is_terminal, attempt_reason, served_model, occurred_at)
		VALUES ($1,$2,$3,'cheap_cloud','gemini','gemini-2.5-flash',$4,$5,$6,$7,$8,$9,$10,$11,$12,'gemini-2.5-flash',$13)`
	if _, err := e.owner.Exec(context.Background(), insert, retry, workspaceID,
		"capture_classify", "retry", 100, 0, 400, "provider_unavailable", logical,
		1, false, "", base.Add(-time.Minute)); err != nil {
		t.Fatalf("seed retry: %v", err)
	}
	if _, err := e.owner.Exec(context.Background(), insert, newest, workspaceID,
		"capture_classify", "terminal", 100, 20, 900, nil, logical,
		2, true, "retry_on_5xx", base); err != nil {
		t.Fatalf("seed terminal: %v", err)
	}
	if _, err := e.owner.Exec(context.Background(), insert, older, workspaceID,
		"enrich", "older", 40, 8, 120, nil, older,
		1, true, "", base.Add(-2*time.Hour)); err != nil {
		t.Fatalf("seed older call: %v", err)
	}
	// A task whose ONLY row is non-terminal (an in-flight first attempt) must
	// never reach the trace filter's task set — the list is terminal-only, so
	// the option would filter to an empty page.
	if _, err := e.owner.Exec(context.Background(), insert, ids.NewV7(), workspaceID,
		"draft_reply", "inflight", 10, 0, 50, "provider_unavailable", ids.NewV7(),
		1, false, "", base.Add(-3*time.Hour)); err != nil {
		t.Fatalf("seed non-terminal-only call: %v", err)
	}
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO ai_call_payload (workspace_id, ai_call_id, request_payload, response_payload)
		VALUES ($1, $2, $3, $4)`, workspaceID, newest,
		`{"system":"classify safely","messages":[{"role":"user","content":"hello"}]}`,
		`"commitment"`); err != nil {
		t.Fatalf("seed payload: %v", err)
	}
	return seededAiCalls{newest: newest, older: older}
}

func TestAiCallsListPagesTerminalCallsNewestFirst(t *testing.T) {
	e := setupWithOptions(t, compose.WithAiPayloadCaptureFlag(true))
	e.bootstrapWorkspace(t)
	seeded := seedAiCallTrace(t, e)

	var first crmcontracts.AiCallListResponse
	if status := e.call(t, http.MethodGet, "/v1/ai/calls?limit=1", nil, nil, &first); status != http.StatusOK {
		t.Fatalf("first page status = %d", status)
	}
	if len(first.Data) != 1 || ids.UUID(first.Data[0].Id) != seeded.newest || !first.Page.HasMore || first.Page.NextCursor == nil {
		t.Fatalf("first page = %+v", first)
	}

	var second crmcontracts.AiCallListResponse
	path := "/v1/ai/calls?limit=1&cursor=" + url.QueryEscape(*first.Page.NextCursor)
	if status := e.call(t, http.MethodGet, path, nil, nil, &second); status != http.StatusOK {
		t.Fatalf("second page status = %d", status)
	}
	if len(second.Data) != 1 || ids.UUID(second.Data[0].Id) != seeded.older {
		t.Fatalf("second page = %+v", second)
	}
	if !first.PayloadCaptureEnabled {
		t.Fatal("payload_capture_enabled = false, want true")
	}
	// The filter option set is complete regardless of page size (this is a
	// limit=1 page) AND terminal-only: it carries capture_classify and enrich
	// sorted, but NOT draft_reply, whose only row is a non-terminal attempt.
	if len(first.Tasks) != 2 || first.Tasks[0] != "capture_classify" || first.Tasks[1] != "enrich" {
		t.Fatalf("first page tasks = %v, want [capture_classify enrich]", first.Tasks)
	}

	for task, wantID := range map[string]ids.UUID{
		"capture_classify": seeded.newest,
		"enrich":           seeded.older,
	} {
		var filtered crmcontracts.AiCallListResponse
		path := "/v1/ai/calls?task=" + url.QueryEscape(task)
		if status := e.call(t, http.MethodGet, path, nil, nil, &filtered); status != http.StatusOK {
			t.Fatalf("task %s status = %d", task, status)
		}
		if len(filtered.Data) != 1 || ids.UUID(filtered.Data[0].Id) != wantID {
			t.Fatalf("task %s data = %+v", task, filtered.Data)
		}
	}
}

func TestAiCallDetailCarriesLadderAndPayload(t *testing.T) {
	e := setupWithOptions(t, compose.WithAiPayloadCaptureFlag(true))
	e.bootstrapWorkspace(t)
	seeded := seedAiCallTrace(t, e)

	var detail crmcontracts.AiCall
	if status := e.call(t, http.MethodGet, "/v1/ai/calls/"+seeded.newest.String(), nil, nil, &detail); status != http.StatusOK {
		t.Fatalf("detail status = %d", status)
	}
	if len(detail.Attempts) != 2 || detail.Attempts[0].Attempt != 1 || !detail.Attempts[1].IsTerminal {
		t.Fatalf("attempt ladder = %+v", detail.Attempts)
	}
	if !detail.PayloadCaptured || detail.Payload == nil {
		t.Fatalf("payload shape = %+v", detail)
	}
	request, ok := detail.Payload.Request.(map[string]interface{})
	if !ok || request["system"] != "classify safely" {
		t.Fatalf("payload request = %#v", detail.Payload.Request)
	}

	var older crmcontracts.AiCall
	if status := e.call(t, http.MethodGet, "/v1/ai/calls/"+seeded.older.String(), nil, nil, &older); status != http.StatusOK {
		t.Fatalf("older detail status = %d", status)
	}
	if older.PayloadCaptured || older.Payload != nil {
		t.Fatalf("older payload = %+v, want absent", older.Payload)
	}
}

func TestAiCallIsInvisibleAcrossTenants(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	seeded := seedAiCallTrace(t, e)

	workspaceB := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'AI Two', 'ai-two', 'EUR')`,
		workspaceB); err != nil {
		t.Fatalf("seed second workspace: %v", err)
	}
	actorID := ids.NewV7()
	ctx := principal.WithCorrelationID(
		principal.WithWorkspaceID(context.Background(), workspaceB), ids.NewV7(),
	)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + actorID.String(), UserID: actorID,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"automation": {Read: true, Update: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	if _, err := ai.NewCallReadStore(e.pool).GetCall(ctx, seeded.newest); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("cross-tenant detail err = %v, want ErrNotFound", err)
	}
}

func TestAiCallsRefusedWithoutAutomationGrant(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	seeded := seedAiCallTrace(t, e)
	demoteToRep(t, e)

	if status := e.call(t, http.MethodGet, "/v1/ai/calls", nil, nil, nil); status != http.StatusForbidden {
		t.Fatalf("rep list status = %d, want 403", status)
	}
	if status := e.call(t, http.MethodGet, "/v1/ai/calls/"+seeded.newest.String(), nil, nil, nil); status != http.StatusForbidden {
		t.Fatalf("rep detail status = %d, want 403", status)
	}
}
