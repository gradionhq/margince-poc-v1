// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A run claimed for resume and then abandoned has no way back: the approval
// claim is one-way, so a process killed mid-loop — or a terminal write that
// failed after the claim — leaves the row 'running' and no redelivery can
// touch it (a second delivery correctly declines to start a second loop of an
// approved mutation). The tenant's scheduling pass closes those rows, which is
// the only accounting they will ever get.
//
// The dangerous mistake here is sweeping too much: an awaiting_approval run is
// waiting on a human and may wait for days, and closing one would discard a
// decision nobody has made yet.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestTheTenantPassClosesAbandonedRunsAndLeavesTheRestAlone(t *testing.T) {
	e := setupRunner(t)
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)

	// Three rows the sweep must treat differently. The stale ones are stamped
	// well past the run wall clock; the fresh one is inside it.
	abandoned := e.seedRun(t, "abandoned", "running", now.Add(-2*time.Hour), nil)
	stillWorking := e.seedRun(t, "still-working", "running", now.Add(-time.Minute), nil)
	// updated_at is stamped at start and never bumped, so a run that uses its
	// whole budget is already a full wall clock old when it writes its outcome.
	// That write must land before the sweep can call the run abandoned.
	finishingUpBudget := e.seedRun(t, "finishing-up", "running", now.Add(-compose.RunWallClock-time.Minute), nil)
	pendingApproval := e.seedApproval(t)
	awaitingHuman := e.seedRun(t, "awaiting-human", "awaiting_approval", now.Add(-30*24*time.Hour), &pendingApproval)

	if err := e.svc.TickWorkspace(e.wsCtx, now); err != nil {
		t.Fatalf("tenant pass: %v", err)
	}

	if status, reason := e.runState(t, abandoned); status != "failed" {
		t.Errorf("the abandoned run is %q, want failed — nothing is coming back to finish it", status)
	} else if reason == "" {
		t.Error("the abandoned run was closed with no degrade_reason, so an operator cannot tell why")
	}
	if status, _ := e.runState(t, stillWorking); status != "running" {
		t.Errorf("a run inside its wall clock is %q, want running — the sweep took a live run", status)
	}
	if status, _ := e.runState(t, finishingUpBudget); status != "running" {
		t.Errorf("a run just past its wall clock is %q, want running — it is still writing its outcome, "+
			"and reporting it abandoned would be a lie about a run a human may have approved", status)
	}
	// The one that would hurt: a human decision still pending.
	if status, _ := e.runState(t, awaitingHuman); status != "awaiting_approval" {
		t.Errorf("a run awaiting a human is %q, want awaiting_approval — a person may take weeks, and "+
			"closing it discards a decision nobody has made", status)
	}
}

// The sweep is an UPDATE with no id predicate — its only filters are status and
// age — so every row in the table is a candidate and nothing in the SQL names a
// tenant. What keeps it inside one workspace is FORCE RLS plus the GUC
// WithWorkspaceTx binds. That is the property the whole shape rests on, so it is
// asserted here rather than inferred from the policy.
func TestTheSweepCannotReachAnotherTenantsRuns(t *testing.T) {
	e := setupRunner(t)
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)

	mine := e.seedRun(t, "mine-abandoned", "running", now.Add(-2*time.Hour), nil)
	otherWS := e.otherWorkspace(t)
	theirs := e.seedRunIn(t, otherWS, "theirs-abandoned", now.Add(-2*time.Hour))

	if err := e.svc.TickWorkspace(e.wsCtx, now); err != nil {
		t.Fatalf("tenant pass: %v", err)
	}

	if status, _ := e.runState(t, mine); status != "failed" {
		t.Errorf("this tenant's abandoned run is %q, want failed", status)
	}
	// Read as the OWNER: the point is whether the row changed at all, and the
	// app role cannot see another workspace's rows to tell us.
	var status string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status FROM agent_run WHERE id = $1`, theirs).Scan(&status); err != nil {
		t.Fatalf("reading the other tenant's run: %v", err)
	}
	if status != "running" {
		t.Errorf("another tenant's run is %q after this workspace's pass, want running — one tenant's "+
			"sweep must not reach another's rows", status)
	}

	// Positive control: that row surviving means nothing unless it was sweepable
	// in the first place. Bound to ITS workspace, the same sweep closes it — so
	// what protected it above was the tenant binding, not some property of the row.
	otherCtx := principal.WithWorkspaceID(context.Background(), otherWS)
	swept, err := e.store.FailStuckRuns(otherCtx, now.Add(-2*compose.RunWallClock), "positive control")
	if err != nil {
		t.Fatalf("sweeping the other tenant under its own binding: %v", err)
	}
	if swept != 1 {
		t.Errorf("the other tenant's own sweep closed %d runs, want 1 — the row above was never sweepable, "+
			"so its survival proved nothing about isolation", swept)
	}
}

// otherWorkspace mints a second tenant to sweep against.
func (e *runnerEnv) otherWorkspace(t *testing.T) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Other Tenant', $2, 'EUR')`,
		id, "reap-other-"+id.String()); err != nil {
		t.Fatalf("seeding the second workspace: %v", err)
	}
	return id
}

// seedRunIn writes an abandoned run into an arbitrary workspace, which seedRun
// cannot do because it stamps this test's own tenant.
func (e *runnerEnv) seedRunIn(t *testing.T, wsID ids.UUID, triggerRef string, updatedAt time.Time) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO agent_run (id, workspace_id, agent_spec, goal, trigger_ref, status, updated_at)
		VALUES ($1, $2, 'morning_brief', 'another tenant''s run', $3, 'running', $4)`,
		id, wsID, triggerRef, updatedAt); err != nil {
		t.Fatalf("seeding a run in workspace %s: %v", wsID, err)
	}
	return id
}

// seedRun writes one agent_run row with an exact updated_at, which is the whole
// input to the sweep's decision and cannot be produced by running a real run.
// An awaiting_approval row must carry its approval and pending snapshot — the
// agent_run_awaiting_shape CHECK refuses a parked run with nothing to resume.
func (e *runnerEnv) seedRun(t *testing.T, triggerRef, status string, updatedAt time.Time, approvalID *ids.UUID) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	var pending any
	if approvalID != nil {
		pending = `{"tool":"update_record","args":{}}`
	}
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO agent_run (id, workspace_id, agent_spec, goal, trigger_ref, status, updated_at,
		                       approval_id, pending)
		VALUES ($1, $2, 'morning_brief', 'seeded for the sweep', $3, $4, $5, $6, $7::jsonb)`,
		id, e.wsID, triggerRef, status, updatedAt, approvalID, pending); err != nil {
		t.Fatalf("seeding a %q run: %v", status, err)
	}
	return id
}

// seedApproval writes the staged row an awaiting_approval run points at.
func (e *runnerEnv) seedApproval(t *testing.T) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO approval (id, workspace_id, kind, proposed_by, target_entity_type, target_entity_id,
		                      summary, proposed_change, diff_hash, expires_at)
		VALUES ($1, $2, 'advance_deal', 'agent:test', 'deal', $3,
		        'staged for the sweep test', '{}'::jsonb, 'sha256:test', now() + interval '30 days')`,
		id, e.wsID, ids.NewV7()); err != nil {
		t.Fatalf("seeding the pending approval: %v", err)
	}
	return id
}

// runState reads a run's status and degrade_reason through the APP role, so the
// read is workspace-scoped exactly as the product's own reads are.
func (e *runnerEnv) runState(t *testing.T, runID ids.UUID) (status, reason string) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.wsCtx, e.pool, func(tx pgx.Tx) error {
		var degrade *string
		if err := tx.QueryRow(context.Background(),
			`SELECT status, degrade_reason FROM agent_run WHERE id = $1`, runID).Scan(&status, &degrade); err != nil {
			return err
		}
		if degrade != nil {
			reason = *degrade
		}
		return nil
	}); err != nil {
		t.Fatalf("reading run %s: %v", runID, err)
	}
	return status, reason
}
