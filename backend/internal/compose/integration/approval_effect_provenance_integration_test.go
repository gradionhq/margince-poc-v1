// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A staging's KIND is not a namespace. The REST admission gate stages under
// the operation's tool name, and compose registers approved-effect executors
// under kinds its own proposal flows mint — "enrich" is both. So an agent
// could mint, by hitting a URL, a staging that a human's approve click would
// hand to the compose enrichment executor, over an envelope that executor
// cannot parse. Worse than a failed write: the executor redeems FIRST, in its
// own committed transaction, so the approval was consumed and could never be
// redeemed again, the human saw a 500, and the audit row recorded a
// redemption for an effect that never ran.
//
// Provenance decides instead: a server-side proposal carries no passport, an
// agent-minted one does, and only the former reaches an executor.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestAgentMintedStagingDoesNotInvokeAServerSideEffect(t *testing.T) {
	e := Setup(t)

	const kind = "enrich"
	var ran int
	svc := approvals.NewService(e.Pool).WithEffect(kind,
		func(context.Context, ids.ApprovalID, json.RawMessage, string) error {
			ran++
			return errors.New("this executor cannot parse an agent REST envelope")
		})

	// The shape the REST gate stages: a canonicalized {operation, path, body}
	// call, under an AGENT principal holding a passport.
	agentCtx := e.AgentCtxWithPassport()
	approvalID, err := svc.Stage(agentCtx, approvals.StageInput{
		Kind:           kind,
		ProposedChange: json.RawMessage(`{"operation":"scrapeCompany","path":"/v1/organizations/x/enrich","body":null}`),
		DiffHash:       "h-" + ids.NewV7().String(),
		Summary:        "Agent REST POST /v1/organizations/x/enrich",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A human approves it. The decision must succeed on its own terms.
	if _, err := svc.Decide(e.Admin(), approvalID, true, nil); err != nil {
		t.Fatalf("approving an agent-minted staging → %v, want ok (the server-side executor is not its business)", err)
	}
	if ran != 0 {
		t.Errorf("the server-side executor ran %d time(s) for an agent-minted staging", ran)
	}

	// And the approval survives for the agent to redeem the ADR-0055 way,
	// by repeating its call — the old path consumed it here.
	if consumed := e.WsCount(t,
		`SELECT count(*) FROM approval WHERE id = $1 AND consumed_at IS NOT NULL`, approvalID); consumed != 0 {
		t.Error("the approval was consumed by an effect that never ran")
	}
}

// The other half: a proposal the SERVER minted still runs its executor, so
// the provenance check narrows the collision rather than disabling the
// confirm-first proposal flows.
func TestServerMintedProposalStillInvokesItsEffect(t *testing.T) {
	e := Setup(t)

	const kind = "enrich"
	var ran int
	svc := approvals.NewService(e.Pool).WithEffect(kind,
		func(context.Context, ids.ApprovalID, json.RawMessage, string) error {
			ran++
			return nil
		})

	approvalID, err := svc.Stage(e.Admin(), approvals.StageInput{
		Kind:           kind,
		ProposedChange: json.RawMessage(`{"organization_id":"018f2a10-0000-7000-8000-000000000001","fields":[]}`),
		DiffHash:       "h-" + ids.NewV7().String(),
		Summary:        "Enrichment proposal for Acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(e.Admin(), approvalID, true, nil); err != nil {
		t.Fatalf("approving a server-minted proposal → %v", err)
	}
	if ran != 1 {
		t.Errorf("the server-side executor ran %d time(s), want 1", ran)
	}
}
