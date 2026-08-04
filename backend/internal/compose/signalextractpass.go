// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What one reading pass did, and when it must stop doing it.
//
// The extractor's work is bounded twice over: by the conversations the queue
// offers, and by the time the scan job has left. This file owns the second
// bound and the report a caller logs; signalextract.go owns the reading.

import (
	"context"
	"errors"
	"fmt"
	"time"
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
// conversation. A pass with no deadline never stops here.
func outOfTime(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	return ok && time.Until(deadline) <= extractStopMargin
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
