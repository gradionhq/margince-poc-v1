// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// What the connection row learns from a turn, and when that member is next worth
// asking.
//
// SPLIT FROM poll.go, which owns the turn itself, because this file holds the one
// number in the unit that is a SAFETY BOUND rather than a preference: a member not
// polled inside the server's retention window loses those messages permanently, so
// the backoff ceiling trades provider load against data loss. That argument deserves
// to be findable, and it is easier to find in a file named for it.
//
// EVERY DEADLINE HERE IS THE DATABASE'S. poll_after is written as `now() + interval`
// and compared against `now()`, so nothing in this unit reads a wall clock. A value
// written from the application's clock and compared against the server's is a
// scheduling bug that appears only when the two drift — and appears as a member who
// is never due again.

import (
	"context"
	"errors"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// recordTurn writes what the turn learned, in a transaction of its own — opened
// after every ingest has returned, which is the rule this file exists to keep.
//
// ONE STATEMENT FOR BOTH OUTCOMES, so "last_polled_at is written even on a
// failure" is structural rather than a second path somebody can forget: the
// cursor, the class and the status all arrive as the values the row SHOULD now
// carry, and a clean turn is the one where the class is empty.
func recordTurn(ctx context.Context, rt extension.Runtime, conn connection, turn pollOutcome) error {
	cadence, cadenceArgs := cadenceAfter(turn, conn)
	args := append([]any{conn.ID, turn.status, turn.errorClass, conn.Version}, cadenceArgs...)
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET status = $2, last_error_class = NULLIF($3, ''),
			        last_polled_at = now(), version = version + 1, updated_at = now()`+cadence+`
			  WHERE id = $1::uuid AND version = $4
			 RETURNING `+connectionColumns, args...).Scan)
		if err != nil {
			if errors.Is(err, extension.ErrNoRows) {
				// The row moved between the fleet read and here — the member
				// reconnected, or another writer touched it. What this turn
				// learned describes a connection that no longer exists in the
				// state it was read in, and writing it would undo what that
				// writer did.
				//
				// THIS IS NOT WHAT PROTECTS A WITHDRAWAL. Whether anything may
				// be captured at all was decided before the landing pass, by
				// decideAgainst re-reading the member's consent after the drain;
				// this guard only declines to overwrite a row that has moved on.
				// Records that landed before that point stay, exactly as a
				// captured message stays after any later erasure — removing one
				// is the core's erasure path, not a connector's.
				return nil
			}
			return err
		}
		if !turn.landed && updated.Status == conn.Status &&
			updated.LastErrorClass == conn.LastErrorClass {
			// A turn that moved nothing is a poll that found nothing, and
			// recording it would write one ledger row per member per cadence
			// forever to say that a schedule ran. The touched column is the
			// timestamp, which is not a fact anybody will later ask who changed.
			return nil
		}
		return recordConnection(ctx, tx, extension.AuditUpdate, turnVerb(turn), &conn, &updated)
	})
}

// cadenceAfter is when this member is next due, and it is the ONE place the three
// answers are chosen between.
//
// A TURN THAT FAILED TOUCHES NEITHER COLUMN, and that is the recommendation this
// unit makes rather than an omission. Incrementing the idle counter on a failure
// would conflate "nothing to say" with "cannot ask", so a member whose session died
// and was then repaired would carry a fake idle history and be polled slowly for
// hours after they fixed it. And the case that genuinely must not be retried on a
// cadence — a credential Zalo no longer accepts — is ALREADY fully backed off by a
// different mechanism: that turn parks the row as needs_reconnect, and the fleet
// read above admits only `connected`, so the member leaves the tick entirely until
// a human re-scans. What is left is a transient failure, and that is precisely the
// case where waiting longer buys nothing and risks the retention window.
func cadenceAfter(turn pollOutcome, conn connection) (clause string, args []any) {
	switch {
	case turn.failed:
		return "", nil
	case turn.landed:
		// A conversation that just started moving is the one worth watching.
		return ", " + duePromptly, nil
	}
	return `, idle_streak = idle_streak + 1, poll_after = now() + $5::interval`,
		[]any{backoffFor(conn.IdleStreak + 1).String()}
}

// basePollInterval is the cadence api/jobs.yaml declares for this unit's tick,
// restated because a Go function cannot read the contract. A test parses the
// fragment and holds the two equal rather than trusting this copy.
const basePollInterval = 300 * time.Second

// maxPollBackoff IS A CORRECTNESS BOUND, NOT A TUNING PREFERENCE, and this is the
// argument for the number.
//
// A member not polled inside the server's retention window loses those messages
// permanently — there is no acknowledgement on this protocol and no since-parameter,
// so the queue is the whole of the backlog and it expires. Every increase here
// therefore trades provider load against DATA LOSS, which is not a trade a cadence
// setting usually makes.
//
// Retention is CLAIMED to be three days (`retention_time: 259201000`). It has been
// MEASURED once, at about an hour, and DESIGN §9.1 is explicit that this is thin
// evidence. So the ceiling is derived from the measurement and not from the claim: a
// cap sized against three days would lose a quiet member's messages outright if the
// true window is the hour somebody actually observed.
//
// Fifteen minutes, with the invariant that the worst gap between two drains of one
// member — this cap plus a whole tick's wall clock, for a member polled at the end
// of one tick and the start of the next — stays at most half of that measured hour.
// A factor of two under a SINGLE observation is the margin; it is not generous
// because the observation is not a guarantee.
//
// WHOEVER FINALLY MEASURES RETENTION OWNS THIS CONSTANT (issue #1692). If the three
// days hold up, this can grow by an order of magnitude and the handshake cost this
// exists to cut mostly disappears. Until then it stays here, and a test holds the
// invariant above rather than the arithmetic.
const maxPollBackoff = 15 * time.Minute

// measuredRetentionFloor is the only retention anybody has observed: one message
// held for about an hour. It is NOT the claimed window, and the difference between
// the two is the whole reason maxPollBackoff is minutes rather than hours.
const measuredRetentionFloor = time.Hour

// backoffFor is how long a member waits after `streak` consecutive drains that
// produced nothing new.
//
// GEOMETRIC FROM THE BASE CADENCE AND CAPPED. Geometric because the question being
// answered — "is this person in a conversation right now?" — gets less likely to
// change the longer the answer has been no, so doubling reaches a useful reduction
// in a handful of ticks rather than a hundred. Capped because of the paragraph above.
//
// The loop stops at the ceiling rather than doubling and clamping afterwards, so a
// large streak cannot overflow the duration on its way to being capped.
func backoffFor(streak int) time.Duration {
	if streak <= 0 {
		return 0
	}
	wait := basePollInterval
	for at := 1; at < streak && wait < maxPollBackoff; at++ {
		wait *= 2
	}
	if wait > maxPollBackoff {
		return maxPollBackoff
	}
	return wait
}

// turnVerb is what the bus is told about this turn. A session that needs a human
// with a phone is a different announcement from an ordinary poll, because it is
// the one another listener may want to act on.
func turnVerb(turn pollOutcome) string {
	if turn.status == statusNeedsReconnect {
		return eventReconnectNeeded
	}
	return eventPolled
}

// failureClass names what went wrong in THIS unit's vocabulary. Zalo's own
// message is deliberately not carried: the class is rendered on a member's
// screen, and a remote party's prose is not this installation's to display.
func failureClass(cause error) string {
	switch {
	case errors.Is(cause, extension.ErrForbidden):
		return "session_withdrawn"
	case errors.Is(cause, extension.ErrInvalid):
		return "connection_unusable"
	case errors.Is(cause, context.DeadlineExceeded), errors.Is(cause, errUnanswered):
		return "provider_unavailable"
	default:
		return "poll_failed"
	}
}
