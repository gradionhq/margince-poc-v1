// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The 🟡 staging path (AUTO-T05): before this fix, ApplyActions returned
// a bare apperrors.ErrRequiresApproval for a 🟡 action — no approval row
// was ever created, so the run parked at requires_approval forever with
// nothing in `detail` for a human's rejection to find (workflows_blocked.go's
// MarkRunBlocked matches on detail->>'approval_id'). This suite proves the
// whole loop end to end over a real migrated Postgres: a 🟡 firing creates
// a real `approval` row, the parked run's detail names it, and rejecting
// it lands the run 'blocked' with a readable reason.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// testApprovalsAdapter mirrors compose's own (unexported) automation
// staging adapter: this suite lives outside package compose, so it needs
// its own copy of the same six-line mapping onto approvals.Service.
type testApprovalsAdapter struct{ svc *approvals.Service }

func (a testApprovalsAdapter) Stage(ctx context.Context, in automation.StageRequest) (ids.ApprovalID, error) {
	return a.svc.Stage(ctx, approvals.StageInput{
		Kind:           in.Kind,
		ProposedChange: in.ProposedChange,
		DiffHash:       in.DiffHash,
		TargetType:     in.TargetType,
		TargetID:       in.TargetID,
		Summary:        in.Summary,
	})
}

// yellowStagingProbe is a synthetic 🟡 handler that exists only for this
// suite: none of the shipped starters carries a 🟡 action yet (every
// seeded template is 🟢; assign_lead_owner and the lead-score recompute
// are always-on system invariants with no approval-tier action either),
// so nothing else currently exercises ApplyActions' 🟡 branch against a
// real database.
type yellowStagingProbe struct {
	approvals automation.Approvals
	toStage   ids.UUID
}

func (yellowStagingProbe) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "task10_yellow_staging_probe",
		Trigger: workflow.Trigger{EventType: "deal.stage_changed"},
		Tier:    mcp.TierYellow,
	}
}

func (yellowStagingProbe) Match(context.Context, workflow.Event) (bool, error) { return true, nil }

func (p yellowStagingProbe) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	args, err := json.Marshal(map[string]any{"to_stage_id": p.toStage})
	if err != nil {
		return workflow.Effect{}, err
	}
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionAdvanceDeal, Target: ev.Entity, Args: args,
	}}}, nil
}

func (p yellowStagingProbe) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	// A zero-value Provider: a 🟡 action stages instead of writing, so
	// this proves ApplyActions never reaches the write side on this path
	// (workflows.go).
	applied, err := automation.ApplyActions(ctx, automation.Executors{Approvals: p.approvals}, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (yellowStagingProbe) IdempotencyKey(ev workflow.Event) string {
	return "task10_yellow_staging_probe:" + ev.ID.String()
}

func TestYellowActionStagesARealApprovalAndRejectionBlocksTheRun(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	pipeline, open, won := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Yellow Probe Deal", pipeline, open, nil)

	svc := approvals.NewService(e.Pool)
	engine := compose.NewWorkflowEngine(e.Pool)
	engine.RegisterSystemWorkflow(yellowStagingProbe{
		approvals: testApprovalsAdapter{svc: svc},
		toStage:   won.UUID,
	})

	ctx := context.Background()
	if err := engine.HandleEvent(ctx, kevents.Envelope{
		EventID: ids.NewV7(), Type: "deal.stage_changed", WorkspaceID: e.WS,
		OccurredAt: time.Now().UTC(),
		Entity:     kevents.EntityRef{Type: "deal", ID: dealID},
	}); err != nil {
		t.Fatal(err)
	}

	var status string
	var approvalIDStr, reason *string
	readRun := func() {
		t.Helper()
		if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`SELECT status, detail->>'approval_id', detail->>'reason'
				 FROM workflow_run WHERE handler = 'task10_yellow_staging_probe'`,
			).Scan(&status, &approvalIDStr, &reason)
		}); err != nil {
			t.Fatal(err)
		}
	}
	readRun()

	if status != "requires_approval" {
		t.Fatalf("run status = %q, want requires_approval", status)
	}
	if approvalIDStr == nil || *approvalIDStr == "" {
		t.Fatal("run detail carries no approval_id — this is the AUTO-T05 dead end: a parked run nothing can ever find")
	}
	approvalID, err := ids.ParseAs[ids.ApprovalKind](*approvalIDStr)
	if err != nil {
		t.Fatalf("run detail's approval_id %q does not parse as a UUID: %v", *approvalIDStr, err)
	}

	// The id in `detail` must name a REAL, live approval row, not merely a
	// string shaped like one.
	var kind, approvalStatus string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT kind, status FROM approval WHERE id = $1`, approvalID).Scan(&kind, &approvalStatus)
	}); err != nil {
		t.Fatalf("no approval row behind the run's approval_id: %v", err)
	}
	if kind != string(workflow.ActionAdvanceDeal) || approvalStatus != "pending" {
		t.Fatalf("approval row = (kind=%q, status=%q), want (advance_deal, pending)", kind, approvalStatus)
	}

	// Reject: Decide emits approval.decided on the SAME outbox the
	// workspace's real relay/worker consumes. Read back the envelope it
	// actually wrote and feed it through the engine, exactly as the
	// worker's cg:workflows subscription would — never a hand-built
	// stand-in for what emit() produces. The decider must be a SEEDED
	// user: approval.decided_by carries the composite (workspace_id,
	// decided_by) FK, so a throwaway e.Admin() id would fail it.
	rejectReason := "not the right stage for this deal"
	decider := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)
	if _, err := svc.Decide(decider, approvalID, false, &rejectReason); err != nil {
		t.Fatalf("rejecting the staged approval: %v", err)
	}
	var envelopeJSON []byte
	if err := owner.QueryRow(context.Background(),
		`SELECT envelope FROM event_outbox WHERE envelope->>'type' = 'approval.decided'
		 AND envelope->'entity'->>'id' = $1 ORDER BY id DESC LIMIT 1`,
		approvalID.String()).Scan(&envelopeJSON); err != nil {
		t.Fatalf("reading back the emitted approval.decided envelope: %v", err)
	}
	var decidedEnv kevents.Envelope
	if err := json.Unmarshal(envelopeJSON, &decidedEnv); err != nil {
		t.Fatalf("decoding the emitted envelope: %v", err)
	}
	if err := engine.HandleEvent(ctx, decidedEnv); err != nil {
		t.Fatal(err)
	}

	readRun()
	if status != "blocked" {
		t.Fatalf("run status after rejection = %q, want blocked", status)
	}
	if reason == nil || !strings.Contains(*reason, approvalID.String()) {
		t.Fatalf("blocked reason = %v, want it to name the rejected approval %s", reason, approvalID)
	}
}
