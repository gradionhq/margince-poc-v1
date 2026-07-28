// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// How one dispatch attempt is RECORDED. The sequence next door decides what an
// attempt concluded; these four write that conclusion to the delivery row and
// render it as the return signature every call site is one line of. They live
// apart so the file next door reads as the sequence rather than as the
// bookkeeping under it.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// outcomeUndecided is the zero Outcome: a step that reached no verdict and
// leaves the delivery to the next one. It never leaves this package.
const outcomeUndecided Outcome = ""

// park ends a delivery no retry repairs, recording why in words an operator
// can act on. THIS disposition's wait is always zero, because parking asks for
// nothing to be tried again; postpone and throttled return a real interval.
// What all four share is the return SIGNATURE, which is what keeps their call
// sites one line each.
//
// ErrTerminal from the transition means a newer attempt already closed this
// row: a benign no-op, so this attempt reports that it did nothing rather than
// claiming a park it did not perform.
func (d *Dispatcher) park(ctx context.Context, id ids.UUID, reason string) (Outcome, time.Duration, error) {
	if err := d.store.Park(ctx, id, reason); err != nil {
		if errors.Is(err, ErrTerminal) {
			return OutcomeSkipped, 0, nil
		}
		return OutcomeRetry, 0, fmt.Errorf("comms: parking delivery: %w", err)
	}
	return OutcomeParked, 0, nil
}

// throttled defers a delivery the PROVIDER refused for now, honouring the
// interval it stated.
//
// It KEEPS the attempt, unlike the pacing deferral below, and the difference is
// not stylistic: the message reached the provider — a 429 is an answer from it,
// not a failure to ask — so this was a transmission attempt to both readers of
// the counter, and it must be bounded like one. Giving the rung back here would
// leave a throttled delivery snoozing forever: MailboxRatePolicy meters only
// successful sends, so the policy chain keeps saying "go now" and never reaches
// its own maximum-age park, and the caller's ladder is restored by the very
// snooze this asks for. Nothing else would ever move the row.
//
// The ladder end is checked HERE rather than left to the guard in
// DispatchWithWait for two reasons. That guard runs before transmit, so it
// would only fire one dispatch later — asking the provider to be re-tried on a
// rung that no longer exists. And it parks under a reason that names no cause,
// where an operator reading this row should learn which side stopped the
// message.
func (d *Dispatcher) throttled(ctx context.Context, del Delivery, retryAfter time.Duration) (Outcome, time.Duration, error) {
	// del.Attempts counts this attempt, and the guard above admitted it, so
	// this is the last rung exactly when no further one remains.
	if del.Attempts >= d.maxAttempts-1 {
		return d.park(ctx, del.ID, fmt.Sprintf(
			"the provider is rate limiting this mailbox and the retry ladder is exhausted after %d attempts", del.Attempts,
		))
	}
	if err := d.store.RecordFailure(ctx, del.ID, "waiting: the provider is rate limiting this mailbox"); err != nil {
		if errors.Is(err, ErrTerminal) {
			return OutcomeSkipped, 0, nil
		}
		return OutcomeRetry, 0, fmt.Errorf("comms: recording the provider throttle: %w", err)
	}
	return OutcomePostponed, retryAfter, nil
}

// retry records why this attempt failed and hands the cause back so the
// caller's ladder can back off. The delivery stays pending: this is a fault,
// not a verdict.
//
// The row gets faultReason's vetted sentence; the CAUSE goes to the caller,
// which is what puts the full diagnosis in the job log. The split is the point:
// a job log is an operator's own record of a run, while the reason column is a
// durable field read alongside the message — and read by whoever can read the
// delivery, which is not the same audience.
func (d *Dispatcher) retry(ctx context.Context, id ids.UUID, cause error) (Outcome, time.Duration, error) {
	if err := d.store.RecordFailure(ctx, id, faultReason(cause)); err != nil {
		if errors.Is(err, ErrTerminal) {
			// A newer attempt already owns this row and will report its own
			// outcome. The cause is dropped rather than returned because
			// returning it would put a finished delivery back on the ladder:
			// the fault belongs to this attempt alone and no longer describes
			// the delivery's state. It is lost to the caller's logs, which is
			// the price of not resurrecting a closed delivery.
			return OutcomeSkipped, 0, nil
		}
		return OutcomeRetry, 0, errors.Join(cause, err)
	}
	return OutcomeRetry, 0, cause
}

// faultPrefix labels every fault sentence, so a reason column reads the same
// whether the cause was recognised or not.
const faultPrefix = "transient fault, will retry: "

// unrecognisedFault is what a cause outside the vocabulary below becomes. The
// causes reaching retry include arbitrary infrastructure errors — a wrapped
// database error carries SQL text and table names, a keyvault error names its
// backend — and this column is durable and operator-readable. Bounding such a
// cause was never enough: a truncated SQL statement is still a SQL statement on
// the row. So the row gets a sentence that says what to do, and the cause
// itself goes to the job log through retry's returned error.
const unrecognisedFault = faultPrefix + "the send could not be completed; the job log for this delivery carries the cause"

// faultVocabulary is the closed set of causes whose own meaning may reach the
// delivery's reason column. Every entry is a sentinel this system defines —
// the connector seam's shared failure vocabulary — so the sentence is OURS,
// phrased for an operator, never a provider's or a driver's text passed
// through. Adding an entry is a deliberate act of declaring one more fault
// safe to name on the row.
var faultVocabulary = []struct {
	sentinel error
	reason   string
}{
	{connector.ErrRateLimited, faultPrefix + "the provider is rate limiting this mailbox"},
	{connector.ErrAuthRejected, faultPrefix + "the provider rejected this mailbox's credential"},
	{connector.ErrUnreachable, faultPrefix + "the provider could not be reached"},
}

// faultReason renders a fault as the kind of operator sentence every other
// reason in this package is: a fixed string chosen by what the cause IS, never
// the cause's own text.
func faultReason(cause error) string {
	for _, known := range faultVocabulary {
		if errors.Is(cause, known.sentinel) {
			return known.reason
		}
	}
	return unrecognisedFault
}

// postpone is the PACING deferral, and pace is its only caller: OUR OWN rule
// held the message back and nothing was ever handed to a provider. It records
// which rule, so an operator seeing a deferred message knows what deferred it,
// and hands back the attempt Load counted, because this dispatch transmitted
// nothing. A provider throttle is a different fact and takes the different path
// above (throttled), which keeps its rung.
//
// A pacing postponement must not consume a rung of the retry ladder, on EITHER
// side of the seam. The row's own counter is restored here (RecordDeferral);
// the caller's must be restored too — the exhaustion guard runs AFTER the policy
// chain, so on the last rung a deferral is returned where a park would
// otherwise be, and a caller that implements the wait by burning an attempt
// leaves that row pending with no attempts left and nothing that would ever
// move it. Implement the wait as a reschedule that restores the attempt, never
// as a failed one.
//
// With both restored, a permanently paced delivery is bounded by maxAge alone
// (pace) and parks with a reason that names the pacing — not by the transmit
// ladder, which it never spent.
func (d *Dispatcher) postpone(ctx context.Context, id ids.UUID, reason string, wait time.Duration) (Outcome, time.Duration, error) {
	if err := d.store.RecordDeferral(ctx, id, reason); err != nil {
		if errors.Is(err, ErrTerminal) {
			return OutcomeSkipped, 0, nil
		}
		return OutcomeRetry, 0, fmt.Errorf("comms: recording the deferral: %w", err)
	}
	return OutcomePostponed, wait, nil
}
