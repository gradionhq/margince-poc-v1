// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// What a failed poll is CALLED, and what the tick does about it. Two questions,
// deliberately kept together and deliberately kept out of poll.go, which is
// about walking a provider and landing what it returns.
//
// They are two questions rather than one because the same class means different
// things at different scales. A member whose token was refused is recorded on
// that member's row and the tick carries on — one revoked token must not be why
// nobody else's messages arrive. The same failure across EVERY member is not one
// member's problem at all, and what the tick answers then is what the operator
// reads.
//
// And a name is not a disposition. A provider nobody can reach and a credential
// the provider refuses are both failures, both classified, and only one of them
// needs a human — so only one of them becomes dead work.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// fleetFailure classifies a tick in which EVERY connected member failed.
//
// A SHARED class is reported as itself, and that is the case worth getting right:
// every member failing because the provider's host does not resolve from this
// installation is one outage happening in several places, and reporting it as its
// own class is what turns a screenful of dead jobs into a sentence naming the
// thing to go fix. Answering "everybody failed" there would throw away the one
// fact the tick actually established.
//
// Members failing for DIFFERENT reasons is the genuinely different situation, and
// the class for it says so rather than picking one member's cause to speak for the
// rest. Nothing is common to them, so there is no single outage to chase, and the
// remedy sends the operator to the per-connection classes that do have specific
// answers.
func fleetFailure(ctx context.Context, failed []extension.FailureClass) error {
	cause := fmt.Errorf("relay: all %d connection(s) failed this tick", len(failed))
	shared := failed[0]
	for _, class := range failed[1:] {
		if class.Class != shared.Class {
			return extension.Failure(classEveryMemberFailed, cause)
		}
	}
	return dispositionFor(ctx, shared, cause)
}

// pollRetryDelay is how long a tick postpones itself for when NO member could
// reach the provider. It is the DISPATCHER'S OWN CADENCE (api/jobs.yaml), and the
// match is the design rather than a coincidence.
//
// A postponed child sits in `scheduled`, which is one of the states the fan-out's
// uniqueness window covers, so while it waits the dispatcher's next insert for
// this workspace collapses into it. The postponement therefore REPLACES the tick
// it would have raced, and asking for the cadence is asking for exactly the
// rhythm the connector already has. Said
// exactly: the delay runs from the FAILURE rather than from the schedule, so
// during an outage the effective interval is the cadence plus however long a tick
// spends discovering it cannot reach anybody — strictly slower than health, never
// faster. Slower is the safe direction and the margin is minutes against a
// retention window of days.
//
// It is deliberately NOT a backoff. The cursor did not move, so what a slower
// poll buys is a later recovery in exchange for one saved request every two
// minutes. A ladder would be buildable — River keeps a snooze count in the job's
// own metadata — so this is a decision about which direction is worth going, not
// a limitation.
//
// THE THROTTLE ARM DESERVES ITS OWN SENTENCE. errTransient covers a 429 as well
// as a timeout and a 5xx, and a 429 is a reachable provider asking for less
// traffic rather than one that cannot answer. What makes the same delay right
// there is that it is the HEALTHY cadence: a throttled tick postponing to 120s
// puts no more load on the provider than a successful one does, and it is
// strictly gentler than what it replaces, where River's ladder retried within
// seconds and then discarded the row. What this unit does NOT do is read the
// interval the provider asked for — nothing here parses Retry-After, so a
// provider naming a longer wait is answered on our clock rather than on theirs.
const pollRetryDelay = 120 * time.Second

// dispositionFor decides whether a fleet-wide failure FAILS the tick or postpones
// it, which is a different question from what the failure is called.
//
// A provider nobody in this workspace can reach is the one class whose own remedy
// says that nothing needs doing — no cursor moved, so the next reachable tick
// walks the same regions and loses nothing. Failing it anyway spends the child's
// three attempts and discards the row, and at this cadence an outage of any
// length therefore manufactures dead work every two minutes, each piece of it
// raising a banner that says a human must intervene in work no human can help
// with.
//
// Every other class still fails, INCLUDING the mixed one: members failing for
// different reasons is not an outage waiting to clear, it is several problems
// with several owners, and postponing it would be this unit quietly deciding not
// to tell any of them.
//
// A TICK WHOSE OWN CONTEXT IS DONE IS REFUSED, and this arm is the reason
// dispositionFor takes a context at all. The classification is still right —
// nothing was reached — but the disposition is not. A tick that ran out of wall
// clock did not meet an outage: it met its own window, because there is more work
// here than the window holds, and every later tick spends the same window and
// expires in the same place. Postponing that hides a fan-out that can NEVER
// finish behind a row that looks like it is waiting patiently, with no dead work
// and no error column anywhere to say otherwise; it needs a wider timeout or a
// smaller fan-out, and a human to choose. A CANCELLED context is a role shutting
// down, and postponing that delays the next poll by a whole cadence on every
// restart.
// The tick's context is asked rather than the cause, because the cause cannot
// answer: the transport formats what the HTTP client said as TEXT, so a deadline
// is not reachable through errors.Is by the time it arrives here. Asking the
// context is also the more precise question — it separates OUR clock running out
// from a provider that accepts a connection and then hangs, and only the first is
// a fact about this installation.
func dispositionFor(ctx context.Context, class extension.FailureClass, cause error) error {
	if class.Class == classProviderUnavailable.Class && ctx.Err() == nil {
		return extension.Reschedule(class, pollRetryDelay, cause)
	}
	return extension.Failure(class, cause)
}

// noteFailure records on the row what went wrong, so the screen can say it.
//
// The class is this unit's, never the provider's message. An unauthorized
// connection PARKS: retrying a revoked token on a cadence is how an
// installation rate-limits itself for nothing, and the member has to paste a
// new one anyway.
func noteFailure(ctx context.Context, rt extension.Runtime, conn connection, cause error) error {
	// The TOKEN goes on the row, where the connector screen filters and greps on
	// it; the SENTENCES go to the job surface. They are two halves of one declared
	// class, so neither surface can drift into a vocabulary of its own.
	class, status := failureClass(cause), statusConnected
	if errors.Is(cause, errUnauthorized) {
		status = statusReauth
	}
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		// On the version this poll READ, so a failure from a poll that started
		// before the member pasted a working token cannot park the connection
		// they just repaired.
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET status = $2, last_error_class = $3, last_polled_at = now(),
			        version = version + 1, updated_at = now()
			  WHERE id = $1::uuid AND version = $4
			 RETURNING `+connectionColumns, conn.ID, status, class.Class, conn.Version).Scan)
		if err != nil {
			if isNoRows(err) {
				return nil
			}
			return err
		}
		verb := eventPolled
		if status == statusReauth {
			verb = eventReauth
		}
		return recordConnection(ctx, tx, extension.AuditUpdate, verb, &conn, &updated)
	})
}

// failureClass names what went wrong in this unit's own vocabulary — the token a
// screen filters on and the two sentences an operator reads, as one declared
// value (failureclasses.go). The provider's text is deliberately not carried: a
// remote party's prose is not this installation's to display or to publish.
func failureClass(cause error) extension.FailureClass {
	switch {
	case errors.Is(cause, errUnauthorized):
		return classTokenRejected
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
