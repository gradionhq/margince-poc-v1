// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The ONE fleet enumeration. Before this file every sweep carried its own
// copy of the workspace scan and its own inline per-workspace loop, which
// meant a failed tenant pass became a log line inside a job row River then
// recorded as completed — success on the outside, silent failure within.
// Dispatching instead gives each workspace its own row to succeed or fail
// as, and leaves exactly one place where the fleet is enumerated at all.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// enumerateWorkspaces reads every live workspace. This is the sanctioned
// fleet scan for a pass that works on behalf of an active tenant.
func enumerateWorkspaces(ctx context.Context, pool *pgxpool.Pool) ([]ids.UUID, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before any per-workspace tx exists.
	rows, err := pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("compose: enumerating workspaces: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
}

// enumerateEveryWorkspace reads EVERY workspace, archived ones included —
// unlike the passes that work on behalf of a live tenant. Archiving a
// workspace does not un-store the snapshots inside it: skipping those rows
// would keep subject data forever in exactly the workspaces nobody looks at
// any more, and idempotency_key.workspace_id is ON DELETE RESTRICT, so the
// leftovers would also refuse the eventual hard delete.
func enumerateEveryWorkspace(ctx context.Context, pool *pgxpool.Pool) ([]ids.UUID, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; retention reads every tenant, archived included, before any per-workspace tx exists.
	rows, err := pool.Query(ctx, `SELECT id FROM workspace ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("compose: enumerating every workspace: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
}

// The bounded queues a fanned-out pass lands on. MaxWorkers: 5 bounds only
// the default queue, and deep_read and rate_refresh already run alongside it
// on the same process against one pgx pool — so converted sweeps must not
// simply pile onto default.
const (
	// aiCaptureQueue carries the four AI-backed capture passes. Two workers,
	// matching deep_read: the same species of work — long, model-bound, and
	// fine to run behind the short maintenance jobs.
	aiCaptureQueue      = "ai_capture"
	aiCaptureMaxWorkers = 2

	// overlayReconcileQueue is serial. See the queue table in NewJobRunner
	// for why per-workspace parallelism is not what this phase is after.
	overlayReconcileQueue = "overlay_reconcile"
)

// sweepWorkspaceMaxAttempts is deliberately small. A fanned-out pass's real
// retry cadence is the dispatcher's tick: a workspace that fails is
// re-enqueued on the next pass. River's ladder is here only to ride out a
// transient blip within one tick window.
//
// Leaving it unset would silently replace the tick with River's default
// ladder of 25 attempts on attempt⁴ backoff, which retries far more
// aggressively than the tick early on and far less often later — and,
// because retryable is one of activeSweepStates, a backing-off job would
// suppress the tick's own re-enqueue of that workspace until it discarded.
const sweepWorkspaceMaxAttempts = 3

// workspaceSweepOpts is the enqueue policy for one fanned-out workspace job.
// A kind whose tick is slower than the ladder would run may take a larger
// attempt count, but the number is always chosen against that kind's tick
// interval and the reason recorded where it is passed.
func workspaceSweepOpts(queue string, maxAttempts int) *river.InsertOpts {
	return &river.InsertOpts{
		Queue:       queue,
		MaxAttempts: maxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByState: activeSweepStates},
	}
}

// insertManyFunc is the slice of River's insert surface a dispatch needs.
// Narrowed to the one method so the fan-out's failure posture can be proven
// without a database or a live client.
type insertManyFunc func(ctx context.Context, params []river.InsertManyParams) error

// dispatchPerWorkspace enqueues one job per live workspace, built by argsFor,
// as ONE atomic insert.
func dispatchPerWorkspace(ctx context.Context, pool *pgxpool.Pool, opts *river.InsertOpts, argsFor func(ids.UUID) river.JobArgs) error {
	workspaces, err := enumerateWorkspaces(ctx, pool)
	if err != nil {
		return err
	}
	return dispatchWith(ctx, workspaces, clientInsertMany(ctx), opts, argsFor)
}

// clientInsertMany binds the fan-out to the River client already in context —
// the shape gmailSyncWorker's dispatcher uses.
//
// The client is resolved INSIDE the closure, not when the closure is built:
// river.ClientFromContext panics when there is none, and a dispatch over an
// empty fleet never inserts, so resolving eagerly would turn "no workspaces
// yet" into a panic on a path that has nothing to do.
func clientInsertMany(ctx context.Context) insertManyFunc {
	return func(ctx context.Context, params []river.InsertManyParams) error {
		_, err := river.ClientFromContext[pgx.Tx](ctx).InsertMany(ctx, params)
		return err
	}
}

// dispatchWith is dispatchPerWorkspace over an already-read fleet and an
// injected inserter.
//
// Atomicity is not a nicety here, it is the correctness argument. A
// per-workspace loop of single inserts that fails partway leaves some
// children queued, and the dispatcher then fails and is retried. By the time
// it retries, the children that were enqueued may already have COMPLETED —
// and activeSweepStates deliberately excludes completed (so a finished sweep
// never blocks the next scheduled run), so ByArgs uniqueness does NOT
// suppress them. The retry silently runs those workspaces a second time: a
// second overlay reconcile spending incumbent API quota, a second AI-backed
// capture pass spending model budget.
//
// InsertMany is all-or-nothing, so a dispatch whose INSERT failed enqueued
// nothing and its retry starts from a clean slate. Logging the failure and
// carrying on is the swallowed-error shape this whole phase removes, one level
// up: either the fan-out lands or the dispatcher fails and says so.
//
// What this does NOT buy is exactly-once. River is at-least-once: the insert
// commits in its own transaction, and the dispatcher is marked completed
// afterwards, so a process that dies between the two is rescued and re-runs the
// fan-out over children that may already have completed — which ByArgs
// uniqueness does not suppress, because completed is outside activeSweepStates.
// The bound on that is the workspace passes themselves: each re-reads its own
// backlog and a caught-up one costs a probe. A dispatcher whose children are
// expensive enough to care (overlay, the AI-backed captures) carries its own
// pacing in the row it works from, not in this helper.
func dispatchWith(ctx context.Context, workspaces []ids.UUID, insert insertManyFunc, opts *river.InsertOpts, argsFor func(ids.UUID) river.JobArgs) error {
	if len(workspaces) == 0 {
		return nil
	}
	params := make([]river.InsertManyParams, 0, len(workspaces))
	for _, ws := range workspaces {
		params = append(params, river.InsertManyParams{Args: argsFor(ws), InsertOpts: opts})
	}
	if err := insert(ctx, params); err != nil {
		return fmt.Errorf("compose: dispatching %d workspace jobs: %w", len(params), err)
	}
	return nil
}
