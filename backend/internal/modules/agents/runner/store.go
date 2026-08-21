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
	// db binds the workspace this store runs for (ADR-0091 §9 step 3).
	db  *database.DB
	now func() time.Time
}

// ForWorkspace is this store re-bound to one tenant of the fleet fan-out. The
// scheduler is one long-lived service ticking every workspace in turn, and the
// workspace a row lands in is the handle's, not the ctx's.
func (s *Store) ForWorkspace(ws ids.WorkspaceID) *Store {
	return &Store{db: s.db.ForWorkspace(ws), now: s.now}
}

// NewStore opens the runner's store on a handle already bound to the
// workspace it serves.
func NewStore(db *database.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// StartRun records a run for one trigger occurrence. created=false
// means this occurrence already ran (or is running) — the §6
// idempotency rule; the caller must not start a second loop.
func (s *Store) StartRun(ctx context.Context, spec AgentSpec, triggerRef string, passportID ids.PassportID) (runID ids.UUID, created bool, err error) {
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO agent_run (agent_spec, goal, trigger_ref, passport_id)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (trigger_ref) DO NOTHING
			RETURNING id, created_at, attempt`,
			spec.Name, spec.Goal, triggerRef, passportID)
		var raw string
		var startedAt time.Time
		var attempt int
		scanErr := row.Scan(&raw, &startedAt, &attempt)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil // lost the idempotency race or already ran
		}
		if scanErr != nil {
			return scanErr
		}
		created = true
		if runID, scanErr = ids.Parse(raw); scanErr != nil {
			return scanErr
		}
		return announceActivity(ctx, tx, occurrence{
			spec: spec.Name, triggerRef: triggerRef, state: stateRunning,
			passportID: &passportID, startedAt: &startedAt, attempt: attempt,
		})
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
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var o occurrence
		var result []byte
		err := tx.QueryRow(ctx, `
			UPDATE agent_run SET
			  -- A correction of an already-SETTLED run is a new attempt: the
			  -- sweep and this write are two terminal states for one occurrence,
			  -- and the projection orders them on the attempt. The CASE reads
			  -- the row's OLD status, so an ordinary finish stays attempt 1.
			  attempt = attempt + CASE
			    WHEN status IN ('completed','degraded','failed') THEN 1 ELSE 0 END,
			  status = $2,
			  result = $3,
			  trace = trace || $4::jsonb,
			  pending = $5,
			  approval_id = $6,
			  -- Result.DegradeReason, never DegradeDetail: this column is read by
			  -- the human the run acted for, and the cause is theirs to be
			  -- spared and the operator log's to keep.
			  degrade_reason = NULLIF($7, ''),
			  steps_used = $8,
			  output_tokens = $9,
			  updated_at = now(),
			  finished_at = CASE WHEN $2 IN ('completed','degraded','failed') THEN now() ELSE NULL END
			WHERE id = $1
			RETURNING agent_spec, trigger_ref, passport_id, created_at, finished_at, degrade_reason, result, attempt`,
			runID, status, res.Final, traceJSON, pendingJSON, approvalID,
			res.DegradeReason, res.StepsUsed, res.OutputTokens).
			Scan(&o.spec, &o.triggerRef, &o.passportID, &o.startedAt, &o.finishedAt, &o.degradeReason, &result, &o.attempt)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("runner: run %s not visible in this workspace", runID)
		}
		if err != nil {
			return fmt.Errorf("runner: save outcome: %w", err)
		}
		o.state = runProjectionState[status]
		o.waitingOnAHuman = status == statusAwaitingApproval
		o.summary = summaryOf(result)
		return announceActivity(ctx, tx, o)
	})
}

// MarkFailed closes a run that crashed outside the loop's own degrade
// path (e.g. the brain adapter failed to construct). The reason is a
// FailureReason, never a cause: this column is read by the human the run acted
// for, and the cause belongs in the operator log.
func (s *Store) MarkFailed(ctx context.Context, runID ids.UUID, reason FailureReason) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var o occurrence
		err := tx.QueryRow(ctx, `
			UPDATE agent_run SET status = 'failed', degrade_reason = $2, updated_at = now(), finished_at = now()
			WHERE id = $1
			RETURNING agent_spec, trigger_ref, passport_id, created_at, finished_at, degrade_reason, attempt`,
			runID, string(reason)).
			Scan(&o.spec, &o.triggerRef, &o.passportID, &o.startedAt, &o.finishedAt, &o.degradeReason, &o.attempt)
		if errors.Is(err, pgx.ErrNoRows) {
			// The run is gone or not this workspace's. Unchanged behaviour: the
			// caller's own error path already covers a run it cannot close.
			return nil
		}
		if err != nil {
			return err
		}
		o.state = stateFailed
		return announceActivity(ctx, tx, o)
	})
}

// FailStuckRuns closes the runs that were claimed and then abandoned — a row
// left in 'running' with nothing alive to finish it — and returns the ids it
// closed so the caller can name them.
//
// Such a run is not recoverable, only accountable, which is why this is a sweep
// and not a retry. ClaimSuspendedByApproval is deliberately one-way, so a resume
// that dies — a process killed mid-loop, or a terminal write that failed after
// the claim — leaves the row 'running' and nothing redelivers it: a second
// delivery finds no awaiting_approval row and correctly declines to start a
// second loop of a mutation a human approved once.
//
// grace is measured against the DATABASE clock, and deliberately not against the
// caller's. Every writer of updated_at stamps now() inside the transaction, so a
// cutoff computed on a worker host whose time had run ahead would compare two
// unrelated clocks and fail runs that were still executing. Only 'running' is
// swept: 'awaiting_approval' waits on a human and may wait indefinitely.
func (s *Store) FailStuckRuns(ctx context.Context, grace time.Duration, reason FailureReason) ([]ids.UUID, error) {
	// A grace of zero means "fail every running run", which is one character away
	// from a plausible edit to the caller's constant. The check is on the
	// MICROSECONDS the statement will actually use, not on the duration: an
	// interval is the finest thing Postgres can compare, so a sub-microsecond
	// grace truncates to zero and lands on that same everything-is-abandoned
	// cutoff while reading as a positive duration in Go.
	graceMicros := grace.Microseconds()
	if graceMicros <= 0 {
		return nil, fmt.Errorf("runner: stuck-run grace must be at least a microsecond, got %s", grace)
	}
	var swept []ids.UUID
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE agent_run
			   SET status = 'failed', degrade_reason = $2, updated_at = now(), finished_at = now()
			 WHERE status = 'running'
			   AND updated_at < now() - ($1 * interval '1 microsecond')
			RETURNING id, agent_spec, trigger_ref, passport_id, created_at, finished_at, degrade_reason, attempt`,
			graceMicros, string(reason))
		if err != nil {
			return err
		}
		// Materialized before anything is announced: the announce writes on the
		// same connection, and a partly-consumed pgx.Rows cannot share it.
		var sweptOccurrences []occurrence
		for rows.Next() {
			var id ids.UUID
			var o occurrence
			if err := rows.Scan(&id, &o.spec, &o.triggerRef, &o.passportID,
				&o.startedAt, &o.finishedAt, &o.degradeReason, &o.attempt); err != nil {
				rows.Close()
				return err
			}
			o.state = stateFailed
			swept = append(swept, id)
			sweptOccurrences = append(sweptOccurrences, o)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, o := range sweptOccurrences {
			if err := announceActivity(ctx, tx, o); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("runner: fail stuck runs: %w", err)
	}
	return swept, nil
}

// SuspendedRun is a parked run keyed by its approval — what the
// approval.decided consumer needs to resume it.
type SuspendedRun struct {
	RunID      ids.UUID
	SpecName   string
	Goal       string
	TriggerRef string
	PassportID ids.PassportID
	Pending    Pending
}

// ClaimSuspendedByApproval resolves an approval decision to its parked run
// and CLAIMS it in the same statement: the row leaves awaiting_approval as
// it is read, so exactly one delivery of a given decision can resume it.
//
// Reading without claiming was not safe here. The bus is at-least-once and
// nothing downstream is idempotent: a resumed run is a fresh multi-step
// loop, not an upsert by natural key, and the row only left
// awaiting_approval when that whole loop finished — up to the run's full
// wall clock later. A redelivery, a reclaim by a peer worker, or a restart
// mid-resume therefore found the run still resumable and started a SECOND
// loop from the same Pending and the same spent-budget baseline, so the
// per-run ceilings were enforced per goroutine rather than per run, and the
// two races to write the outcome.
//
// A claimed run that then dies leaves the row in 'running' with nothing to
// resume it — the deliberate direction to fail in, since the alternative is
// executing an approved mutation twice.
//
// Not-found is a normal answer: most approvals are not runner stagings, and
// a second delivery of one that is now reads the same way.
func (s *Store) ClaimSuspendedByApproval(ctx context.Context, approvalID ids.ApprovalID) (SuspendedRun, bool, error) {
	var run SuspendedRun
	var pendingJSON []byte
	found := false
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			UPDATE agent_run SET status = 'running', updated_at = now()
			WHERE approval_id = $1 AND status = 'awaiting_approval'
			RETURNING id, agent_spec, goal, trigger_ref, passport_id, pending`, approvalID)
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
		return SuspendedRun{}, false, fmt.Errorf("runner: claim suspended run: %w", err)
	}
	return run, found, nil
}

// QueuedJob is one claimed queue entry.
type QueuedJob struct {
	ID         ids.UUID
	SpecName   string
	TriggerRef string
	PassportID *ids.PassportID
}

// EnqueueJob seeds one trigger occurrence; re-seeding is a no-op.
func (s *Store) EnqueueJob(ctx context.Context, specName, triggerRef string, passportID *ids.PassportID, dueAt time.Time) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		// DO NOTHING means this occurrence is already seeded, and re-announcing
		// it would tell the projection about a queue entry that did not change.
		var seeded bool
		err := tx.QueryRow(ctx, `
			INSERT INTO runner_job (agent_spec, trigger_ref, passport_id, due_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (agent_spec, trigger_ref) DO NOTHING
			RETURNING true`,
			specName, triggerRef, passportID, dueAt).Scan(&seeded)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("runner: enqueue job: %w", err)
		}
		return announceActivity(ctx, tx, occurrence{
			spec: specName, triggerRef: triggerRef, state: stateQueued, passportID: passportID,
		})
	})
}

// ClaimDueJobs atomically claims up to limit due jobs. FOR UPDATE SKIP LOCKED
// keeps parallel workers from double-claiming without serializing on each
// other.
func (s *Store) ClaimDueJobs(ctx context.Context, limit int) ([]QueuedJob, error) {
	var jobs []QueuedJob
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
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
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var o occurrence
		var settledAt time.Time
		err := tx.QueryRow(ctx, `
			UPDATE runner_job SET status = $2, last_error = NULLIF($3, ''), agent_run_id = $4
			WHERE id = $1
			RETURNING agent_spec, trigger_ref, passport_id, now()`,
			jobID, status, failReason, runID).
			Scan(&o.spec, &o.triggerRef, &o.passportID, &settledAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("runner: finish job: %w", err)
		}
		// A job that ends WITH a run announces nothing: the run row is the
		// occurrence's authority from the moment it exists, and it has already
		// reported its own outcome. A job that ends WITHOUT one — no passport
		// bound, an unknown spec, a failed claim — is the only account of that
		// occurrence there will ever be, and saying nothing leaves the rail
		// showing it queued forever.
		if runID != nil || failReason == "" {
			return nil
		}
		o.state = stateFailed
		o.startedAt = &settledAt
		o.finishedAt = &settledAt
		o.degradeReason = &failReason
		return announceActivity(ctx, tx, o)
	})
}
