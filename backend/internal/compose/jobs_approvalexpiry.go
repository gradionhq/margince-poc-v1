// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The sweep that turns a staging's closed window into a decision.
//
// Expiry has always been a reading — effectiveStatus folds it in, so a stale
// item displays as expired and the row still says pending. That reading is
// enough to stop the item being decided and enough for nothing else: nobody is
// told, nothing is audited, and an automation parked behind it waits on a
// verdict that has already gone against it.
//
// This is the tick that writes it down. The decision itself belongs to the
// approvals module — status, the system-actor audit row, the event — and this
// file supplies only the clock, because a policy that fires on a schedule still
// needs something to be scheduled.
//
// It reaches no other module, which is worth saying because the obvious version
// does: see Work below for why the run transition travels on the event instead.
//
// One job, not a dispatcher and a child (ADR-0103 §1): the pass is one indexed
// scan over an installation-wide table, so there is nothing to fan out over.

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ApprovalExpiryArgs closes the window on stagings nobody decided.
type ApprovalExpiryArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (ApprovalExpiryArgs) Kind() string { return "approval_expiry" }

// InsertOpts carries the attempt cap the declaration publishes, because the
// periodic insert supplies uniqueness and no attempt policy of its own.
//
// One attempt: this pass's retry is its own next tick, and every row it did not
// reach is still due then — the predicate is a clock, and a clock does not need
// a second rung to come back to something.
func (ApprovalExpiryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       river.QueueDefault,
		MaxAttempts: 1,
		UniqueOpts:  river.UniqueOpts{ByState: activeSweepStates},
	}
}

type approvalExpiryWorker struct {
	pool     *pgxpool.Pool
	identity *identity.Service
	log      *slog.Logger
}

// Work expires what is due. It ends no runs itself, and that is deliberate.
//
// The obvious shape is a second loop here calling automation's transition for
// every approval this tick expired. It would work most of the time and leave a
// window: a crash between the two would abandon a run behind an approval that
// is no longer pending, so the next tick — which scans for PENDING rows — would
// never come back to it. A comment explaining that gap is not a design.
//
// The gap does not need to exist. Each expiry emits approval.decided in the
// SAME transaction that writes it, through the outbox, and automation already
// consumes that event to end a parked run. So the transition is carried by a
// delivery guarantee this file does not have to reproduce: at-least-once,
// wrapped in the consumer's own dedupe, retried until it lands.
//
// Which leaves this worker with one job, and no cross-module edge at all.
func (w *approvalExpiryWorker) Work(ctx context.Context, _ *river.Job[ApprovalExpiryArgs]) error {
	passCtx, err := installationJobCtx(ctx, w.identity)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	// The clock is the actor. Every expiry writes an audit row and an event, and
	// both need a principal on the context — but no human decided any of this,
	// so binding one would put somebody's name on refusals they never made. The
	// system id says exactly what happened, and the correlation id groups one
	// tick's expiries as the single pass they are.
	passCtx = principal.WithActor(passCtx, principal.Principal{
		Type: principal.PrincipalSystem, ID: approvals.ExpiryActor,
	})
	passCtx = principal.WithCorrelationID(passCtx, ids.NewV7())
	expired, err := expiringApprovalsService(w.pool).ExpireDue(passCtx)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	if len(expired) > 0 {
		// Worth a line: an expiry is a refusal nobody chose, so a run of them is
		// the shape of work going unanswered rather than a healthy sweep.
		w.logger().InfoContext(ctx, "approval expiry: stagings closed unactioned", "count", len(expired))
	}
	return nil
}

func (w *approvalExpiryWorker) logger() *slog.Logger {
	if w.log == nil {
		return slog.Default()
	}
	return w.log
}
