// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The durable half of the MCP Tasks extension: the agent_task rows behind the
// handle a staged confirm-first call hands back.
//
// The store lives here rather than in modules/agents for the reason the
// idempotency claim does — that package declares seams and owns no SQL — and a
// task is operational state of the same class as the claim: it records what a
// surface did, never what the business knows. So no audit row, no outbox row.
// The effect a task performs is executed through Registry.Invoke and carries
// the full write shape there.
//
// EVERY QUERY IS SCOPED TO THE CALLING PASSPORT, in the SQL rather than by a
// caller remembering to filter. A task belonging to another passport is absent,
// not forbidden: distinguishing the two would tell a caller which ids exist.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// agentTasks implements the tool surface's task seam.
type agentTasks struct{ pool *pgxpool.Pool }

var _ agents.Tasks = agentTasks{}

// toolTasks is the task store every composed tool surface installs.
func toolTasks(pool *pgxpool.Pool) agentTasks { return agentTasks{pool: pool} }

// taskColumns is the projection every read shares, so three queries cannot
// disagree about what a task is.
const taskColumns = `id, approval_id, tool, status, COALESCE(status_message, ''), result, error,
	served_records, expires_at, created_at, updated_at`

// taskPassport answers the passport a task method is acting under.
//
// A ZERO id is refused as hard as a missing principal. principal.Actor answers
// ok for a zero-value Principal, and a passport-less task row would be one row
// every such caller in the workspace shares — which is precisely the binding
// that makes a task id worthless to anyone but its owner.
func taskPassport(ctx context.Context, verb string) (ids.UUID, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.PassportID.IsZero() {
		return ids.Nil, fmt.Errorf("compose: no agent passport %s an MCP task", verb)
	}
	return actor.PassportID, nil
}

// Create records the handle. It commits before answering, which is the
// specification's own requirement: a client must never have to poll
// speculatively for its own task to appear.
func (t agentTasks) Create(ctx context.Context, in agents.NewTask) (agents.Task, error) {
	passport, err := taskPassport(ctx, "creating")
	if err != nil {
		return agents.Task{}, err
	}
	var task agents.Task
	err = database.WithWorkspaceTx(ctx, t.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO agent_task (workspace_id, approval_id, passport_id, tool, status_message, expires_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, NULLIF($4, ''), $5)
			RETURNING `+taskColumns,
			in.ApprovalID, passport, in.Tool, in.StatusMessage, in.ExpiresAt)
		return scanTask(row, &task)
	})
	if err != nil {
		return agents.Task{}, fmt.Errorf("compose: creating an MCP task: %w", err)
	}
	return task, nil
}

// Load answers this passport's live task. An expired one is already gone as far
// as this server is concerned — the specification permits discarding it — so it
// reads as absent rather than as a task nobody can use.
func (t agentTasks) Load(ctx context.Context, id ids.UUID) (agents.Task, error) {
	passport, err := taskPassport(ctx, "reading")
	if err != nil {
		return agents.Task{}, err
	}
	var task agents.Task
	err = database.WithWorkspaceTx(ctx, t.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+taskColumns+`
			FROM agent_task WHERE id = $1 AND passport_id = $2 AND expires_at > now()`, id, passport)
		return scanTask(row, &task)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return agents.Task{}, apperrors.ErrNotFound
	}
	if err != nil {
		return agents.Task{}, fmt.Errorf("compose: reading an MCP task: %w", err)
	}
	return task, nil
}

// Claim takes a working task for execution.
//
// The UPDATE is the whole concurrency argument: two polls racing here produce
// one row affected and one zero, and the loser reports `working` rather than
// executing a second time. `reclaimed` is the row's own memory that somebody
// held this claim before and never settled — the only local evidence that an
// execution died mid-flight, since the approval cannot tell a poll that crashed
// from a direct call that redeemed it.
func (t agentTasks) Claim(ctx context.Context, id ids.UUID, lease time.Duration) (agents.TaskClaim, error) {
	passport, err := taskPassport(ctx, "claiming")
	if err != nil {
		return agents.TaskClaim{}, err
	}
	var claim agents.TaskClaim
	err = database.WithWorkspaceTx(ctx, t.pool, func(tx pgx.Tx) error {
		// `prior` reads the row as it was BEFORE the update, because every CTE
		// in one statement sees the same snapshot. That is the only way to learn
		// what claimed_at held: RETURNING answers the new value, which this
		// statement has just overwritten.
		return tx.QueryRow(ctx, `
			WITH prior AS (
				SELECT claimed_at FROM agent_task WHERE id = $1 AND passport_id = $2
			), taken AS (
				UPDATE agent_task SET claimed_at = now(), updated_at = now()
				WHERE id = $1 AND passport_id = $2 AND status = 'working' AND expires_at > now()
				  AND (claimed_at IS NULL OR claimed_at < now() - $3::interval)
				RETURNING id
			)
			SELECT EXISTS (SELECT 1 FROM taken),
			       COALESCE((SELECT claimed_at IS NOT NULL FROM prior), false)`,
			id, passport, lease).Scan(&claim.Won, &claim.Reclaimed)
	})
	if err != nil {
		return agents.TaskClaim{}, fmt.Errorf("compose: claiming an MCP task: %w", err)
	}
	// A claim not won says nothing about who held it: another poll is executing
	// right now, which is not the interrupted case and must not be reported as
	// one.
	if !claim.Won {
		return agents.TaskClaim{}, nil
	}
	return claim, nil
}

// Settle records a terminal state, and answers the task as it now stands.
//
// The status guard is what keeps a terminal state immutable. A settle arriving
// for an already-settled task changes nothing and returns what is stored, so
// the first answer a client was given stays the answer.
func (t agentTasks) Settle(ctx context.Context, id ids.UUID, s agents.Settlement) (agents.Task, error) {
	passport, err := taskPassport(ctx, "settling")
	if err != nil {
		return agents.Task{}, err
	}
	var task agents.Task
	err = database.WithWorkspaceTx(ctx, t.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			UPDATE agent_task SET status = $3, status_message = NULLIF($4, ''),
				result = $5, error = $6, served_records = $7, updated_at = now()
			WHERE id = $1 AND passport_id = $2 AND status = 'working'
			RETURNING `+taskColumns,
			id, passport, string(s.Status), s.StatusMessage,
			nullableJSON(s.Result), nullableJSON(s.Error), s.ServedRecords)
		if scanErr := scanTask(row, &task); !errors.Is(scanErr, pgx.ErrNoRows) {
			return scanErr
		}
		// Already terminal. The specification makes that state immutable, so
		// what is stored IS the answer — including for the caller that just
		// tried to overwrite it.
		return scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+`
			FROM agent_task WHERE id = $1 AND passport_id = $2`, id, passport), &task)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return agents.Task{}, apperrors.ErrNotFound
	}
	if err != nil {
		return agents.Task{}, fmt.Errorf("compose: settling an MCP task: %w", err)
	}
	return task, nil
}

// Cancel settles a task NOTHING IS EXECUTING.
//
// The claim guard is the whole of it, and it is what a status check alone could
// not give: a task inside its released call would otherwise be marked cancelled,
// discarding that call's own settlement and telling the client nothing happened
// while the effect was committing.
//
// It is the SAME predicate Claim and the retention sweep use — a live claim is
// one younger than the executor's lease — because all three are asking one
// question, and a cancel with a stricter answer would leave a task whose
// executor died permanently uncancellable. So losing here means an executor
// really is inside the row, and that poll will settle the task itself:
// cancellation is cooperative, and the specification permits a terminal state
// other than cancelled.
func (t agentTasks) Cancel(ctx context.Context, id ids.UUID, lease time.Duration, message string) (bool, error) {
	passport, err := taskPassport(ctx, "cancelling")
	if err != nil {
		return false, err
	}
	var cancelled bool
	err = database.WithWorkspaceTx(ctx, t.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			WITH taken AS (
				UPDATE agent_task SET status = 'cancelled', status_message = NULLIF($4, ''), updated_at = now()
				WHERE id = $1 AND passport_id = $2 AND status = 'working'
				  AND (claimed_at IS NULL OR claimed_at < now() - $3::interval)
				RETURNING id
			)
			SELECT EXISTS (SELECT 1 FROM taken)`, id, passport, lease, message).Scan(&cancelled)
	})
	if err != nil {
		return false, fmt.Errorf("compose: cancelling an MCP task: %w", err)
	}
	return cancelled, nil
}

// nullableJSON writes an absent payload as SQL NULL rather than as the two
// bytes `null`, which the terminal-payload CHECK reads as a value that is
// present. A cancelled task carrying `result = 'null'::jsonb` would fail the
// constraint, and it would fail it at the moment a human's cancellation was
// being recorded.
func nullableJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func scanTask(row pgx.Row, into *agents.Task) error {
	var (
		approvalID ids.UUID
		status     string
		result     []byte
		failure    []byte
	)
	if err := row.Scan(&into.ID, &approvalID, &into.Tool, &status, &into.StatusMessage,
		&result, &failure, &into.ServedRecords, &into.ExpiresAt, &into.CreatedAt, &into.UpdatedAt); err != nil {
		return err
	}
	into.ApprovalID = ids.From[ids.ApprovalKind](approvalID)
	into.Status = agents.TaskStatus(status)
	into.Result, into.Error = json.RawMessage(result), json.RawMessage(failure)
	return nil
}
