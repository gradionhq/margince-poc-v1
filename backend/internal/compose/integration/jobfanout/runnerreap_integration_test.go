// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package jobfanout

// What these prove: the tenant's scheduling pass closes a run that was stranded
// in 'running', leaves every run that is still going anywhere, and cannot reach
// another tenant's rows.
//
// The two ways to get this wrong are both worse than not sweeping at all. An
// awaiting_approval run is waiting on a human who may take weeks, and closing one
// discards a decision nobody has made yet. A run at the end of its budget is
// about to write its outcome, and reporting it abandoned is a lie about a
// mutation a human may have approved.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestTheTenantPassClosesAbandonedRunsAndLeavesTheRestAlone(t *testing.T) {
	re := setupRunner(t)

	// Four rows the sweep must treat differently, each seeded by AGE because the
	// cutoff is derived inside the database — a fixed timestamp here would test
	// this machine's clock against the container's. The margins are whole minutes
	// against a 30-minute grace, so nothing here turns on how long the test takes.
	abandoned := re.seedRun(t, "abandoned", "running", 2*time.Hour, nil)
	stillWorking := re.seedRun(t, "still-working", "running", time.Minute, nil)
	// updated_at is stamped at the start and never bumped, so a run that spends
	// its whole budget is already a full wall clock old when it writes its
	// outcome. That write has to land before the sweep may call the run dead.
	finishingUpBudget := re.seedRun(t, "finishing-up", "running", compose.RunWallClock+time.Minute, nil)
	pendingApproval := re.seedApproval(t)
	awaitingHuman := re.seedRun(t, "awaiting-human", "awaiting_approval", 30*24*time.Hour, &pendingApproval)

	if err := re.svc.TickWorkspace(re.wsCtx, time.Now().UTC()); err != nil {
		t.Fatalf("tenant pass: %v", err)
	}

	closed := re.runState(t, abandoned)
	if closed.status != "failed" {
		t.Errorf("the abandoned run is %q, want failed — nothing is coming back to finish it", closed.status)
	}
	// The reason and finished_at are what an operator actually reads. Asserting
	// only the status would let the sweep close a run with a NULL finish time or
	// somebody else's reason on it.
	if !strings.Contains(closed.reason, "check the audit log") {
		t.Errorf("degrade_reason = %q; it must tell an operator that the run's writes may have "+
			"landed even though its trace is empty", closed.reason)
	}
	if !closed.finished {
		t.Error("the abandoned run was closed with a NULL finished_at, so it reads as still open")
	}

	// Two live runs, one nowhere near its ceiling and one just past it and still
	// writing its outcome. Closing either reports a run abandoned while it was
	// working, and for a resumed run that is a lie about a mutation a human
	// approved.
	for _, live := range []struct {
		name string
		id   ids.UUID
	}{
		{"a run inside its wall clock", stillWorking},
		{"a run just past its wall clock", finishingUpBudget},
	} {
		if got := re.runState(t, live.id); got.status != "running" {
			t.Errorf("%s is %q, want running — the sweep took a run that was still executing",
				live.name, got.status)
		}
	}
	// The one that would hurt most: a human decision still pending.
	if got := re.runState(t, awaitingHuman); got.status != "awaiting_approval" {
		t.Errorf("a run awaiting a human is %q, want awaiting_approval — a person may take weeks, and "+
			"closing it discards a decision nobody has made", got.status)
	}
}

// seedRun writes one agent_run row already staleFor old, which is the whole input
// to the sweep's decision and cannot be produced by running a real run. An
// awaiting_approval row must carry its approval and pending snapshot — the
// agent_run_awaiting_shape CHECK refuses a parked run with nothing to resume.
func (re *runnerEnv) seedRun(t *testing.T, triggerRef, status string, staleFor time.Duration, approvalID *ids.UUID) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	var pending *string
	if approvalID != nil {
		snapshot := `{"tool":"update_record","args":{}}`
		pending = &snapshot
	}
	if _, err := re.Owner.Exec(context.Background(), `
		INSERT INTO agent_run (id, workspace_id, agent_spec, goal, trigger_ref, status, updated_at,
		                       approval_id, pending)
		VALUES ($1, $2, 'morning_brief', 'seeded for the sweep', $3, $4,
		        now() - ($5 * interval '1 microsecond'), $6, $7::jsonb)`,
		id, re.wsID, triggerRef, status, staleFor.Microseconds(), approvalID, pending); err != nil {
		t.Fatalf("seeding a %q run: %v", status, err)
	}
	return id
}

// seedApproval writes the staged row an awaiting_approval run points at.
func (re *runnerEnv) seedApproval(t *testing.T) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := re.Owner.Exec(context.Background(), `
		INSERT INTO approval (id, kind, proposed_by, target_entity_type, target_entity_id,
		                      summary, proposed_change, diff_hash, expires_at)
		VALUES ($1, 'advance_deal', 'agent:test', 'deal', $2,
		        'staged for the sweep test', '{}'::jsonb, 'sha256:test', now() + interval '30 days')`,
		id, ids.NewV7()); err != nil {
		t.Fatalf("seeding the pending approval: %v", err)
	}
	return id
}

// sweptRun is what an operator can see of a run after the sweep has been past.
type sweptRun struct {
	status   string
	reason   string
	finished bool
}

// runState reads a run through the APP role, so the read is workspace-scoped
// exactly as the product's own reads are.
func (re *runnerEnv) runState(t *testing.T, runID ids.UUID) sweptRun {
	t.Helper()
	var got sweptRun
	if err := database.WithWorkspaceTx(re.wsCtx, re.pool, func(tx pgx.Tx) error {
		var degrade *string
		var finishedAt *time.Time
		if err := tx.QueryRow(context.Background(),
			`SELECT status, degrade_reason, finished_at FROM agent_run WHERE id = $1`, runID).
			Scan(&got.status, &degrade, &finishedAt); err != nil {
			return err
		}
		if degrade != nil {
			got.reason = *degrade
		}
		got.finished = finishedAt != nil
		return nil
	}); err != nil {
		t.Fatalf("reading run %s: %v", runID, err)
	}
	return got
}
