// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What one reading pass did, and when it must stop doing it.
//
// The pass drives the queue: it takes the conversations that are due and reads
// them one at a time, and it decides when to stop. Its work is bounded twice
// over — by the conversations the queue offers, and by the time the scan job
// has left. This file owns both bounds and the report a caller logs;
// signalextract.go owns one conversation's reading.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ExtractPass is what one workspace's reading pass did.
//
// Raised alone cannot describe a pass. A pass that read two hundred
// conversations and found nothing worth filing is a healthy one; a pass that
// was offered no conversation at all is a broken queue with nothing to say.
// Both raise nothing, so a caller that logs only what was raised cannot tell
// them apart — and the second is the one somebody has to act on.
type ExtractPass struct {
	// Due is how many conversations the queue offered this pass.
	Due int
	// Raised is how many signals were written.
	Raised int
	// AtCap says the queue filled its per-pass limit, so there is very likely
	// more backlog behind it. The first passes over an installation's history
	// are expected to sit here for a few hours.
	AtCap bool
	// Deferred says the workspace's model budget stopped the pass early.
	Deferred bool
	// OutOfTime says the pass ran up against its own deadline and stopped
	// while conversations were still owed a reading.
	OutOfTime bool
}

// extractStopMargin is how much of the pass's deadline is kept in reserve.
//
// A pass stops on its own rather than being killed mid-conversation. Each
// conversation commits its signals and its watermark together, so being cut off
// costs only the one in flight — but it costs it as a FAILED job, which retries
// the whole pass twice more and then discards it. A discarded job is a fault
// somebody should look at, not the ordinary shape of a first run over a
// mailbox's history.
//
// Ten seconds: room for the conversation in flight to commit what it learned,
// without spending a meaningful slice of the pass on caution.
const extractStopMargin = 10 * time.Second

// outOfTime reports whether the pass should stop rather than start another
// conversation, and whether stopping is an ORDINARY end or a cancelled one.
//
// The two are not the same news and must not read the same. Running down the
// deadline is what a first pass over a mailbox's history does every hour: the
// unread conversations are still due and the job succeeded. A cancelled context
// is a process being torn down or a caller giving up, and reporting that as a
// pass that simply ran out of time would tell River the work is done — so the
// job is marked complete and whatever was left is never picked up as a failure.
//
// A pass with no deadline never stops here.
func outOfTime(ctx context.Context) (stop bool, cancelled bool) {
	if err := ctx.Err(); err != nil {
		return true, true
	}
	deadline, ok := ctx.Deadline()
	return ok && time.Until(deadline) <= extractStopMargin, false
}

// readDeadline bounds one conversation's reading so it cannot spend the margin
// the pass is holding back.
//
// The margin alone only decides which readings BEGIN. A single conversation
// retries and escalates tiers inside the model lane, where one call may run for
// as long as ai's own request timeout, so a reading begun with time to spare can
// still arrive at the job's deadline and fault the pass there. Bounded, it is
// cut short instead, and the pass ends on its own terms.
//
// A pass with no deadline reads unbounded.
func readDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline.Add(-extractStopMargin))
}

// RunWorkspace reads every due thread in the workspace already bound in ctx,
// and reports what the pass did.
//
// Each thread commits on its own: its signals and its watermark move together
// or neither does, so a budget stop or a crash costs at most the thread in
// flight, and that one is read again next pass.
func (x *SignalExtractor) RunWorkspace(ctx context.Context, wsID ids.WorkspaceID) (ExtractPass, error) {
	// Before the queue is even read. The deadline belongs to the whole scan
	// job, and the deterministic half has already spent some of it, so there is
	// a real case where nothing is left by the time this pass begins. Asking
	// for the queue anyway would fail the transaction on the deadline and fault
	// a pass whose only news is that it had no time — the exact fault this
	// stop exists to prevent, moved one query earlier.
	if stop, cancelled := outOfTime(ctx); stop {
		if cancelled {
			return ExtractPass{}, fmt.Errorf("signal extract: %w", ctx.Err())
		}
		return ExtractPass{OutOfTime: true}, nil
	}
	now := x.now()
	var due []settledThread
	if err := database.WithWorkspaceTx(ctx, x.pool, func(tx pgx.Tx) error {
		found, err := dueThreads(ctx, tx, now, extractThreadCap)
		due = found
		return err
	}); err != nil {
		return ExtractPass{}, fmt.Errorf("signal extract: %w", err)
	}
	pass := ExtractPass{Due: len(due), AtCap: len(due) >= extractThreadCap}

	raised := 0
	// One thread's failure is not the pass's. Returning on the first error left
	// every conversation behind it unread, and dueThreads orders newest-first,
	// so a thread that keeps failing keeps arriving at the head of the list and
	// nothing after it is ever reached. Each thread is read on its own terms
	// and the failures are reported together.
	var failed []error
	for i, thread := range due {
		// Checked BEFORE the call, not after it fails. Letting the deadline
		// arrive mid-call turns an ordinary partial pass into a fault: every
		// conversation still queued reports its own "context deadline exceeded",
		// the job errors, River retries the whole pass twice and discards it —
		// and none of that noise describes anything wrong. What is left unread
		// is simply still due, because the watermark only moved for the threads
		// that were actually read.
		if stop, cancelled := outOfTime(ctx); stop {
			pass.Raised = raised
			if cancelled {
				// Not a pass that finished early: a caller that stopped asking.
				// Reported so the job is retried rather than recorded as done
				// with conversations it never looked at.
				return pass, fmt.Errorf("signal extract: %w",
					errors.Join(append(failed, ctx.Err())...))
			}
			x.log.InfoContext(ctx, "signal extract: out of time, stopping the pass",
				"conversations_read", i, "still_due", len(due)-i, "raised", raised)
			pass.OutOfTime = true
			return pass, passFailure(failed)
		}
		// The read gets the pass's remaining time MINUS the margin, so the
		// margin is a reserve rather than a hope. Checking the clock before a
		// read only bounds when one STARTS: a single conversation escalates
		// tiers and retries inside the model lane, where one call alone may run
		// for ai.requestTimeout, so a read begun with time to spare can still
		// arrive at the job's deadline and fault the pass there.
		//
		// Cut short by OUR bound, the read is not a failure — it is the same
		// "no time left" the loop above stops on, noticed one conversation
		// later. The job's own deadline is still unspent, which is exactly what
		// tells the two apart.
		readCtx, cancelRead := readDeadline(ctx)
		n, err := x.readThread(readCtx, wsID, thread, now)
		// Read BEFORE the cancel, which would set this error itself and make
		// every provider failure look like a conversation we cut short.
		cutShort := err != nil && readCtx.Err() != nil && ctx.Err() == nil
		cancelRead()
		raised += n
		if cutShort {
			x.log.InfoContext(ctx, "signal extract: out of time mid-conversation, stopping the pass",
				"conversations_read", i, "still_due", len(due)-i, "raised", raised)
			pass.Raised = raised
			pass.OutOfTime = true
			return pass, passFailure(failed)
		}
		if errors.Is(err, ai.ErrBudgetDeferred) {
			// The budget is the WORKSPACE's, so this one does stop the pass:
			// every thread behind it would buy the same refusal.
			x.log.InfoContext(ctx, "signal extract: budget exhausted, stopping the pass", "raised", raised)
			pass.Raised = raised
			pass.Deferred = true
			return pass, passFailure(failed)
		}
		if err != nil {
			failed = append(failed, fmt.Errorf("reading conversation %q: %w", thread.Key, err))
		}
	}
	pass.Raised = raised
	return pass, passFailure(failed)
}

// passFailure names the pass in whatever its conversations reported, or reports
// nothing when none of them failed.
//
// A pass leaves by four doors — out of time, budget exhausted, and the two ends
// of the loop — and the conversations that failed before it left read the same
// through all of them. Wrapping at each door instead let the same failure reach
// River with or without the pass's name on it, depending only on when the pass
// happened to stop.
func passFailure(failed []error) error {
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("signal extract: %w", errors.Join(failed...))
}
