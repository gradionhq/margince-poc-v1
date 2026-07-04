// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Store persists runs and the trigger queue. Every query rides the
// workspace GUC transaction — the worker crosses tenants by iterating
// workspaces, never by bypassing RLS. Runs and jobs are operational
// runner state, not domain records: the domain writes a run performs
// happen inside the tools it calls, which carry the full audit+outbox
// write shape already.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: time.Now}
}

// StartRun records a run for one trigger occurrence. created=false
// means this occurrence already ran (or is running) — the §6
// idempotency rule; the caller must not start a second loop.
func (s *Store) StartRun(ctx context.Context, spec AgentSpec, triggerRef string, passportID ids.UUID) (runID ids.UUID, created bool, err error) {
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO agent_run (workspace_id, agent_spec, goal, trigger_ref, passport_id)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4)
			ON CONFLICT (workspace_id, trigger_ref) DO NOTHING
			RETURNING id`,
			spec.Name, spec.Goal, triggerRef, passportID)
		var raw string
		scanErr := row.Scan(&raw)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil // lost the idempotency race or already ran
		}
		if scanErr != nil {
			return scanErr
		}
		created = true
		runID, scanErr = ids.Parse(raw)
		return scanErr
	})
	if err != nil {
		return ids.Nil, false, fmt.Errorf("runner: start run: %w", err)
	}
	return runID, created, nil
}

// SaveOutcome lands a Run/Resume result on the run row: terminal
// outcomes close it, a suspension parks the snapshot + approval id.
func (s *Store) SaveOutcome(ctx context.Context, runID ids.UUID, res Result) error {
	traceJSON, err := json.Marshal(res.Steps)
	if err != nil {
		return fmt.Errorf("runner: marshal trace: %w", err)
	}
	var pendingJSON []byte
	var approvalID any
	if res.Pending != nil {
		pendingJSON, err = json.Marshal(res.Pending)
		if err != nil {
			return fmt.Errorf("runner: marshal pending: %w", err)
		}
		approvalID = res.Pending.ApprovalID
	}
	status := map[Outcome]string{
		OutcomeCompleted:        "completed",
		OutcomeDegraded:         "degraded",
		OutcomeAwaitingApproval: "awaiting_approval",
	}[res.Outcome]
	if status == "" {
		return fmt.Errorf("runner: unknown outcome %q", res.Outcome)
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE agent_run SET
			  status = $2,
			  result = $3,
			  trace = trace || $4::jsonb,
			  pending = $5,
			  approval_id = $6,
			  degrade_reason = NULLIF($7, ''),
			  steps_used = $8,
			  output_tokens = $9,
			  updated_at = now(),
			  finished_at = CASE WHEN $2 IN ('completed','degraded','failed') THEN now() ELSE NULL END
			WHERE id = $1`,
			runID, status, res.Final, traceJSON, pendingJSON, approvalID,
			res.DegradeReason, res.StepsUsed, res.OutputTokens)
		if err != nil {
			return fmt.Errorf("runner: save outcome: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("runner: run %s not visible in this workspace", runID)
		}
		return nil
	})
}

// MarkFailed closes a run that crashed outside the loop's own degrade
// path (e.g. the brain adapter failed to construct).
func (s *Store) MarkFailed(ctx context.Context, runID ids.UUID, reason string) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE agent_run SET status = 'failed', degrade_reason = $2, updated_at = now(), finished_at = now()
			WHERE id = $1`, runID, reason)
		return err
	})
}

// SuspendedRun is a parked run keyed by its approval — what the
// approval.decided consumer needs to resume it.
type SuspendedRun struct {
	RunID      ids.UUID
	SpecName   string
	Goal       string
	TriggerRef string
	PassportID ids.UUID
	Pending    Pending
}

// FindSuspendedByApproval resolves an approval decision to its parked
// run. Not-found is a normal answer: most approvals are not runner
// stagings.
func (s *Store) FindSuspendedByApproval(ctx context.Context, approvalID ids.UUID) (SuspendedRun, bool, error) {
	var run SuspendedRun
	var pendingJSON []byte
	found := false
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT id, agent_spec, goal, trigger_ref, passport_id, pending
			FROM agent_run
			WHERE approval_id = $1 AND status = 'awaiting_approval'`, approvalID)
		err := row.Scan(&run.RunID, &run.SpecName, &run.Goal, &run.TriggerRef, &run.PassportID, &pendingJSON)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return json.Unmarshal(pendingJSON, &run.Pending)
	})
	if err != nil {
		return SuspendedRun{}, false, fmt.Errorf("runner: find suspended run: %w", err)
	}
	return run, found, nil
}

// QueuedJob is one claimed queue entry.
type QueuedJob struct {
	ID         ids.UUID
	SpecName   string
	TriggerRef string
	PassportID *ids.UUID
}

// EnqueueJob seeds one trigger occurrence; re-seeding is a no-op.
func (s *Store) EnqueueJob(ctx context.Context, specName, triggerRef string, passportID *ids.UUID, dueAt time.Time) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO runner_job (workspace_id, agent_spec, trigger_ref, passport_id, due_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4)
			ON CONFLICT (workspace_id, agent_spec, trigger_ref) DO NOTHING`,
			specName, triggerRef, passportID, dueAt)
		if err != nil {
			return fmt.Errorf("runner: enqueue job: %w", err)
		}
		return nil
	})
}

// ClaimDueJobs atomically claims up to limit due jobs for this
// workspace. FOR UPDATE SKIP LOCKED keeps parallel workers from
// double-claiming without serializing on each other.
func (s *Store) ClaimDueJobs(ctx context.Context, limit int) ([]QueuedJob, error) {
	var jobs []QueuedJob
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE runner_job SET status = 'running', attempts = attempts + 1
			WHERE id IN (
			  SELECT id FROM runner_job
			  WHERE status = 'queued' AND due_at <= now()
			  ORDER BY due_at
			  LIMIT $1
			  FOR UPDATE SKIP LOCKED)
			RETURNING id, agent_spec, trigger_ref, passport_id`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var j QueuedJob
			if err := rows.Scan(&j.ID, &j.SpecName, &j.TriggerRef, &j.PassportID); err != nil {
				return err
			}
			jobs = append(jobs, j)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("runner: claim jobs: %w", err)
	}
	return jobs, nil
}

// FinishJob closes a claimed job; failures keep their reason on the row
// so an operator can see WHY the 06:00 brief never ran.
func (s *Store) FinishJob(ctx context.Context, jobID ids.UUID, runID *ids.UUID, failReason string) error {
	status := "done"
	if failReason != "" {
		status = "failed"
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE runner_job SET status = $2, last_error = NULLIF($3, ''), agent_run_id = $4
			WHERE id = $1`, jobID, status, failReason, runID)
		if err != nil {
			return fmt.Errorf("runner: finish job: %w", err)
		}
		return nil
	})
}
