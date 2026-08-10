// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The governance hole, proved closed through the wired registry rather than
// against the resolver alone.
//
// A unit test can only show that the resolver decides correctly once it is
// handed both endpoints. What it cannot show is that the registry actually
// READS the deal's current stage on the way — the half that was missing, and the
// half a future edit could drop while every unit test stayed green.
//
// So this drives compose.NewRegistry against a real database, as an AGENT
// principal (the confirm-first tier gates agents; a human is admitted by RBAC
// and would prove nothing about the gate).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestAnAgentCannotReopenAClosedDealWithoutAHuman(t *testing.T) {
	e := Setup(t)
	pipeline, open, won := DealFixture(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	human := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	// Created and closed as a HUMAN, which is how a won deal comes to exist.
	deal := createDealForReopen(human, t, registry, e.Rep1, pipeline, open)
	if _, err := registry.Invoke(human, "advance_deal",
		json.RawMessage(`{"deal_id":"`+deal.String()+`","to_stage_id":"`+won.String()+`"}`)); err != nil {
		t.Fatalf("closing the deal as won: %v", err)
	}

	// And now the move that was ungated: back onto an open stage, as an agent.
	//
	// The assertion is the OUTCOME, not the mechanism. What must be true is that
	// no agent reopens a closed deal on its own authority; whether this
	// particular principal is turned away by the approval gate or by an object
	// grant it does not hold is the gate's business, and pinning one of them
	// would make this test fail the day the other one answered first. So: the
	// call is refused, and the deal is still won afterwards.
	agent := reopenAgentCtx(t, e)
	if _, err := registry.Invoke(agent, "advance_deal",
		json.RawMessage(`{"deal_id":"`+deal.String()+`","to_stage_id":"`+open.String()+`"}`)); err == nil {
		t.Fatal("an agent reopened a won deal with no human in the loop — the close date, the " +
			"lost reason and the FX rate frozen at close are all cleared by that move")
	}
	if stage := dealStageOf(human, t, registry, deal); stage != won.String() {
		t.Fatalf("the deal sits in stage %s, want the won stage it was closed in — it was reopened "+
			"despite the refusal", stage)
	}

	// The routine move stays routine: moving between two OPEN stages is not
	// asked for approval, or this fix would have put a human in front of every
	// stage change on the surface. Driven as the human, because the point here
	// is the RESOLVER's verdict rather than the principal's grants.
	openDeal := createDealForReopen(human, t, registry, e.Rep1, pipeline, open)
	if _, err := registry.Invoke(human, "advance_deal",
		json.RawMessage(`{"deal_id":"`+openDeal.String()+`","to_stage_id":"`+open.String()+`"}`)); err != nil {
		t.Fatalf("an open→open move was refused: %v", err)
	}
}

// dealStageOf reads the stage a deal is actually in — the fact that decides
// whether a refusal held.
func dealStageOf(ctx context.Context, t *testing.T, registry *agents.Registry, deal ids.UUID) string {
	t.Helper()
	out, err := registry.Invoke(ctx, "read_record",
		json.RawMessage(`{"record_type":"deal","id":"`+deal.String()+`"}`))
	if err != nil {
		t.Fatalf("reading the deal back: %v", err)
	}
	var record struct {
		Fields struct {
			StageID string `json:"stage_id"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(ToolPayload(t, out), &record); err != nil {
		t.Fatalf("unreadable read_record answer %s: %v", out, err)
	}
	return record.Fields.StageID
}

// createDealForReopen makes one deal in the pipeline's open stage and answers
// with its id.
func createDealForReopen(ctx context.Context, t *testing.T, registry *agents.Registry, owner ids.UUID, pipeline ids.PipelineID, open ids.StageID) ids.UUID {
	t.Helper()
	out, err := registry.Invoke(ctx, "create_record",
		json.RawMessage(`{"record_type":"deal","fields":{"name":"Reopen probe","owner_id":"`+owner.String()+`","pipeline_id":"`+
			pipeline.String()+`","stage_id":"`+open.String()+`"}}`))
	if err != nil {
		t.Fatalf("creating the deal: %v", err)
	}
	var created struct {
		ID ids.UUID `json:"id"`
	}
	if err := json.Unmarshal(ToolPayload(t, out), &created); err != nil {
		t.Fatalf("unreadable create_record answer %s: %v", out, err)
	}
	return created.ID
}

// reopenAgentCtx is a passport principal holding the write scope — the type the
// confirm-first tier actually gates. A human would be admitted by RBAC and would
// prove nothing about it.
func reopenAgentCtx(t *testing.T, e *Env) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:reopen-probe", SeatType: principal.SeatFull,
		OnBehalfOf: e.Rep1, UserID: e.Rep1, PassportID: ids.NewV7(),
		Scopes:      principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite),
		Permissions: AdminPerms,
	})
}
