// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The send_message tool's own loop, end to end over the real database: an
// MCP tools/call → the 🟡 refusal STAGES an approval instead of dead-ending
// (agents.Registry.Invoke's staging branch, registry.go:126-150) → a human
// approves it → the retry carrying approval_id is the ONLY call that reaches
// Handle and performs the send.
//
// channelsend_integration_test.go proves the REST twin of this refusal, but
// that is a DIFFERENT code path (compose/agentgate.go's stageRefusal via
// canonicalRESTCall) that predates this branch and exercises neither
// sendMessageTool.StageInfo nor Invoke's staging branch. This file is the
// one proof of the MCP loop.
//
// It drives Registry.Invoke directly rather than the wire transport: the
// JSON-RPC framing, discovery and admission machinery is already proven end
// to end by mcp_transport_integration_test.go against a different tool, and
// Invoke is the one call every transport — stdio, hosted MCP, Surface B —
// dispatches through (registry.go's package comment). What was missing is
// the send_message-specific stage → approve → redeem loop against real
// Postgres, which is what this test drives.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// sendMessageInvoker builds the SAME governed registry the api role
// composes — including a REAL delivery machinery, so a redeemed send lands
// in comms_outbound instead of refusing on a nil stager — and returns an
// Invoke closure that re-authenticates the passport per call, exactly as
// every transport does (registry.go's Invoke doc: "There is no other path
// to a Handle in this package").
func (c *channelSendEnv) sendMessageInvoker(t *testing.T, agentToken string) func(args string) (string, error) {
	t.Helper()
	ApplyRiverSchema(t)
	inserter, err := jobs.NewInserter(c.Pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("jobs.NewInserter: %v", err)
	}
	registry := compose.NewRegistry(c.Pool, compose.SendPath{
		Delivery: compose.NewDeliveryStager(c.Pool, inserter),
	})
	authSvc := identity.NewService(c.Pool)
	return func(args string) (string, error) {
		wsID, err := authSvc.InstallationWorkspace(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx := principal.WithWorkspaceID(context.Background(), wsID.UUID)
		agent, err := authSvc.AuthenticateAgent(ctx, agentToken)
		if err != nil {
			t.Fatal(err)
		}
		ctx = principal.WithCorrelationID(principal.WithActor(ctx, agent.Principal()), ids.NewV7())
		out, invokeErr := registry.Invoke(ctx, "send_message", json.RawMessage(args))
		return string(out), invokeErr
	}
}

// TestSendMessageMCPLoopStagesApprovesAndRedeemsAgainstRealPostgres proves
// the loop finding 2 found unproven: a send-scoped agent's tools/call
// refusal stages a REAL approval row and sends nothing; a human's approval
// makes it decidable; the retry carrying approval_id is the only call that
// reaches Handle and produces the outbound activity plus its staged
// delivery; and the redemption is single-use.
func TestSendMessageMCPLoopStagesApprovesAndRedeemsAgainstRealPostgres(t *testing.T) {
	c := setupChannelSend(t)
	token := c.mintPassport(t, []string{"read", "send"})
	invoke := c.sendMessageInvoker(t, token)

	args := fmt.Sprintf(`{"activity_id":%q,"body":"Yes — shipping Monday.","consent_purpose":"transactional"}`, c.activityID)

	_, err := invoke(args)
	var staged *workflow.StagedApprovalError
	if !errors.As(err, &staged) {
		t.Fatalf("first send_message call → %v, want a StagedApprovalError", err)
	}
	if staged.ApprovalID.IsZero() {
		t.Fatal("StagedApprovalError carries a zero approval id")
	}
	c.assertNoOutboundEffect(t, "a staged-but-not-yet-approved send_message call")

	if status := c.Call(t, "POST", "/v1/approvals/"+staged.ApprovalID.String()+"/approve", apptest.AnyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("human approve → %d", status)
	}

	retryArgs := fmt.Sprintf(
		`{"activity_id":%q,"body":"Yes — shipping Monday.","consent_purpose":"transactional","approval_id":%q}`,
		c.activityID, staged.ApprovalID.String())
	out, retryErr := invoke(retryArgs)
	if retryErr != nil {
		t.Fatalf("approved retry → %v, want it to reach Handle and send", retryErr)
	}
	var sent struct {
		ActivityID string `json:"activity_id"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(ToolPayload(t, json.RawMessage(out)), &sent); err != nil {
		t.Fatalf("send_message result does not decode: %v (%s)", err, out)
	}
	if sent.Status != "accepted" {
		t.Fatalf("send_message result = %+v, want status accepted", sent)
	}

	// The delivery names THIS activity (channelsend.go's outboundChannelMessage
	// doc), so the id the tool call handed back must be the same id the staged
	// delivery anchors on — otherwise the response could name any activity while
	// the delivery transmits a different one.
	var deliveryActivity string
	if err := inWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT activity_id::text FROM comms_outbound WHERE channel_user_id IS NOT NULL`).Scan(&deliveryActivity)
	}); err != nil {
		t.Fatalf("reading the staged delivery's activity: %v", err)
	}
	if deliveryActivity != sent.ActivityID {
		t.Fatalf("the staged delivery anchors activity %s, want the one the response named (%s)", deliveryActivity, sent.ActivityID)
	}

	if n := c.stagedChannelDeliveries(t); n != 1 {
		t.Fatalf("%d channel deliveries staged after the approved retry, want 1", n)
	}
	if n := c.outboundActivities(t); n != 1 {
		t.Fatalf("%d outbound activities logged after the approved retry, want 1", n)
	}

	// Exactly-once: the approval was consumed by the retry above, so
	// replaying the identical call must not send a second time.
	if _, replayErr := invoke(retryArgs); replayErr == nil {
		t.Fatal("replaying a consumed approval succeeded; redemption must be single-use")
	}
	if n := c.stagedChannelDeliveries(t); n != 1 {
		t.Fatalf("%d channel deliveries staged after replaying a consumed approval, want still 1", n)
	}
	if n := c.outboundActivities(t); n != 1 {
		t.Fatalf("%d outbound activities logged after replaying a consumed approval, want still 1", n)
	}
}
