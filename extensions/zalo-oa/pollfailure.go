// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// What a failed poll is CALLED, and what the tick does about it. Two questions,
// deliberately kept together and deliberately kept out of poll.go, which is
// about walking the provider and landing what it returns.
//
// A NAME IS NOT A DISPOSITION, and that is the distinction this file exists to
// hold. Every failure below is classified; only one of them needs nobody, and
// only that one postpones itself instead of becoming dead work. Two of the rest
// PARK the connection, which is a third disposition again — the class decides
// which, because a credential the provider rejects is fixed by re-authorizing
// and a PACKAGE the provider says is too low is fixed by paying for an upgrade,
// and sending an operator to do one when they need the other is a wasted
// afternoon in another company.
//
// dispact-connector's pollfailure.go is the same file for the same reason; the
// two connectors should read alike about the one thing they share.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// noteFailure records on the row what went wrong, so the screen can say it.
//
// The class is this unit's, never the provider's message. Two of the outcomes
// PARK the connection rather than retrying it, and which one they park as is the
// distinction the whole error catalog is built around: a credential the provider
// rejects is fixed by re-authorizing, and a PACKAGE the provider says is too low
// is fixed by paying for an upgrade. Sending an operator to do one when they need
// the other is a wasted afternoon in another company.
func noteFailure(ctx context.Context, rt extension.Runtime, conn connection, cause error) error {
	class, status := failureClass(cause), statusConnected
	switch {
	case errors.Is(cause, errUnauthorized):
		status = statusReauth
	case errors.Is(cause, errTierTooLow), errors.Is(cause, errAPINotRegistered):
		status = statusTierLapse
	}
	if status != statusConnected {
		// The TOKEN, not the sentence: the row's column is what the connector
		// screen filters and greps on, and the sentence is what the job surface
		// renders. They are two halves of one declared class, so neither surface
		// can drift into a vocabulary of its own.
		if _, err := park(ctx, rt, conn, class.Class, status); err != nil {
			return errors.Join(cause, err)
		}
		return nil
	}
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		// On the version this poll READ, so a failure from a tick that started
		// before an admin re-authorized cannot mark the connection they just
		// repaired.
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET last_error_class = $2, last_polled_at = now(),
			        version = version + 1, updated_at = now()
			  WHERE id = $1::uuid AND version = $3
			 RETURNING `+connectionColumns, conn.ID, class.Class, conn.Version).Scan)
		if err != nil {
			if isNoRows(err) {
				// The row moved on without this tick. What it learned is about a
				// connection in a state that no longer exists, and the tick that
				// moved it will report its own outcome.
				return nil
			}
			return err
		}
		// RECORDED, like every other state change on this row. The unit's ledger
		// header names exactly one exemption — the poll's last_polled_at touch on
		// an otherwise unchanged row — and this is not it: what is written here is
		// the class a screen renders, and "when did this start failing" is the
		// question a human brings to it.
		return recordConnection(ctx, tx, extension.AuditUpdate, eventPolled, &conn, &updated)
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	// The failure is on the row for the screen to render; the tick's own outcome
	// is the failure, because this installation has exactly one connection and a
	// tick that could not poll it did not do its job.
	//
	// CLASSIFIED on the way out, so the class the row already carries survives the
	// trip to the job surface too. The job layer refuses to persist a cause's own
	// text — river_job.errors has no workspace and every admin reads it — and
	// without the wrapper it had nothing left to report but that the failure could
	// not be classified. The cause still travels underneath, so errors.Is keeps
	// working for everything that classifies on the sentinels, and the unit name
	// stays on the log line the wrapper prints.
	return dispositionFor(class, fmt.Errorf("zalo-oa: the poll failed: %w", cause))
}

// pollRetryDelay is how long a tick postpones itself for when the provider could
// not be reached. It is the DISPATCHER'S OWN CADENCE (api/jobs.yaml), and the
// match is the design rather than a coincidence.
//
// A postponed child sits in `scheduled`, which is one of the states the fan-out's
// uniqueness window covers, so while it waits the dispatcher's next insert for
// this workspace collapses into it. The postponement therefore REPLACES the tick
// it would have raced, and asking for the cadence is asking for exactly the
// rhythm the connector already has — an outage changes what a tick reports, not
// how often it runs.
//
// It is deliberately NOT a backoff, and that is a decision about LOSS rather than
// about politeness. This provider drops messages from its API after roughly nine
// days with no webhook and no depth to page back to (see poll.go's header), so
// polling less during an outage widens the window this connector can permanently
// fall behind by — in exchange for saving one request every two minutes. A ladder
// would be buildable: River keeps a snooze count in the job's own metadata, and
// this unit's own row could hold one. It is refused because it is the wrong
// direction to go, not because there is nothing to build it from.
//
// THE THROTTLE ARM DESERVES ITS OWN SENTENCE, because it is the one case where
// "the provider is refusing anyway" is not the argument. errTransient covers a
// 429 as well as a timeout and a 5xx, and a 429 is a reachable provider asking
// for less traffic. What makes the same delay right there is that it is the
// HEALTHY cadence: a throttled tick postponing to 120s puts no more load on the
// provider than a successful one does, and it is strictly gentler than the
// behaviour it replaces, where River's ladder retried within seconds and then
// discarded the row. What this unit does NOT do is read the interval the provider
// asked for — nothing here parses Retry-After, so a provider naming a longer wait
// is answered on our clock rather than on theirs.
const pollRetryDelay = 120 * time.Second

// dispositionFor decides whether a classified failure FAILS the tick or postpones
// it, which is a different question from what the failure is called.
//
// A provider nobody can reach is the one class whose own remedy says that nothing
// needs doing — the cursor did not move, so the next reachable tick walks the same
// region and loses nothing. Failing it anyway spends the child's three attempts
// and discards the row, and at this cadence an outage of any length therefore
// manufactures dead work every two minutes, each piece of it raising a banner
// that says a human must intervene in work no human can help with.
//
// Every other class still fails, and that asymmetry is the point: a rejected
// credential, a lapsed package and an answer this connector cannot read all need
// somebody, and a postponement would be this unit quietly deciding not to tell
// them.
func dispositionFor(class extension.FailureClass, cause error) error {
	if class.Class == classProviderUnavailable.Class {
		return extension.Reschedule(class, pollRetryDelay, cause)
	}
	return extension.Failure(class, cause)
}

// failureClass names what went wrong in this unit's own vocabulary — the token a
// screen filters on and the two sentences an operator reads, as one declared
// value (failureclasses.go). The provider's text is deliberately not carried: a
// remote party's prose is not this installation's to display or to publish.
func failureClass(cause error) extension.FailureClass {
	switch {
	case errors.Is(cause, errUnauthorized):
		return classTokenRejected
	case errors.Is(cause, errTierTooLow):
		return classPackageTooLow
	case errors.Is(cause, errAPINotRegistered):
		return classAPINotRegistered
	case errors.Is(cause, errTransient):
		return classProviderUnavailable
	case errors.Is(cause, errProvider):
		return classProviderAnswerUnusable
	case errors.Is(cause, extension.ErrForbidden):
		return classMemberNotPermitted
	case errors.Is(cause, extension.ErrInvalid):
		return classConnectionUnusable
	default:
		return classPollFailed
	}
}
