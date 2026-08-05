// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// The sweep is an UPDATE with no id predicate — its only filters are status and
// age — so every row in the table is a candidate and nothing in the SQL names a
// tenant. What keeps it inside one workspace is FORCE RLS plus the app.workspace_id
// GUC that WithWorkspaceTx binds. That is the property the whole shape rests on,
// so it is asserted here rather than inferred from reading the policy.
func TestTheSweepCannotReachAnotherTenantsRuns(t *testing.T) {
	re := setupRunner(t)

	mine := re.seedRun(t, "mine-abandoned", "running", 2*time.Hour, nil)
	otherWS := re.otherWorkspace(t)
	theirs := re.seedRunIn(t, otherWS, "theirs-abandoned", 2*time.Hour)

	if err := re.svc.TickWorkspace(re.wsCtx, time.Now().UTC()); err != nil {
		t.Fatalf("tenant pass: %v", err)
	}

	if got := re.runState(t, mine); got.status != "failed" {
		t.Errorf("this tenant's abandoned run is %q, want failed", got.status)
	}
	if status := re.statusAsOwner(t, theirs); status != "running" {
		t.Errorf("another tenant's run is %q after this workspace's pass, want running — one tenant's "+
			"sweep must not reach another's rows", status)
	}

	// Positive control, and the whole reason the assertion above means anything:
	// that row surviving proves isolation only if the same pass WOULD have closed
	// it. Run through the product's own entry point rather than a cutoff spelled
	// out again here, so the control cannot drift away from the grace it mirrors.
	otherCtx := principal.WithWorkspaceID(context.Background(), otherWS)
	if err := re.svc.TickWorkspace(otherCtx, time.Now().UTC()); err != nil {
		t.Fatalf("the other tenant's own pass: %v", err)
	}
	if status := re.statusAsOwner(t, theirs); status != "failed" {
		t.Errorf("the other tenant's own pass left its abandoned run %q, want failed — that row was never "+
			"sweepable, so its survival above proved nothing about isolation", status)
	}
}

// otherWorkspace mints a second tenant to sweep against.
func (re *runnerEnv) otherWorkspace(t *testing.T) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := re.owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Other Tenant', $2, 'EUR')`,
		id, "reap-other-"+id.String()); err != nil {
		t.Fatalf("seeding the second workspace: %v", err)
	}
	return id
}

// seedRunIn writes an abandoned run into an arbitrary workspace, which seedRun
// cannot do because it stamps this test's own tenant.
func (re *runnerEnv) seedRunIn(t *testing.T, wsID ids.UUID, triggerRef string, staleFor time.Duration) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := re.owner.Exec(context.Background(), `
		INSERT INTO agent_run (id, workspace_id, agent_spec, goal, trigger_ref, status, updated_at)
		VALUES ($1, $2, 'morning_brief', 'another tenant''s run', $3, 'running',
		        now() - ($4 * interval '1 microsecond'))`,
		id, wsID, triggerRef, staleFor.Microseconds()); err != nil {
		t.Fatalf("seeding a run in workspace %s: %v", wsID, err)
	}
	return id
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
	if _, err := re.owner.Exec(context.Background(), `
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
	if _, err := re.owner.Exec(context.Background(), `
		INSERT INTO approval (id, workspace_id, kind, proposed_by, target_entity_type, target_entity_id,
		                      summary, proposed_change, diff_hash, expires_at)
		VALUES ($1, $2, 'advance_deal', 'agent:test', 'deal', $3,
		        'staged for the sweep test', '{}'::jsonb, 'sha256:test', now() + interval '30 days')`,
		id, re.wsID, ids.NewV7()); err != nil {
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

// statusAsOwner reads a run bypassing the workspace binding, because the question
// about another tenant's row is whether it changed at all — and the app role
// cannot see it to answer that either way.
func (re *runnerEnv) statusAsOwner(t *testing.T, runID ids.UUID) string {
	t.Helper()
	var status string
	if err := re.owner.QueryRow(context.Background(),
		`SELECT status FROM agent_run WHERE id = $1`, runID).Scan(&status); err != nil {
		t.Fatalf("reading run %s as the owner: %v", runID, err)
	}
	return status
}
