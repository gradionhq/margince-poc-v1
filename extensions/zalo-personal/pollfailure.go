// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// What a failed drain is CALLED, and what the tick does about it. Two questions,
// deliberately kept together and deliberately kept out of poll.go, which is about
// draining a member's inbox and landing what it returns.
//
// They are two questions rather than one because the same class means different
// things at different scales. A member whose session Zalo no longer accepts is
// recorded on that member's row and the tick carries on — one dead session must
// not be why nobody else's messages arrive. The same failure across EVERY member
// is not one person's problem at all, and what the tick answers then is what the
// operator reads.
//
// And a name is not a disposition. Zalo being unreachable and a session Zalo has
// stopped accepting are both failures, both classified, and only one of them
// needs a human — so only one of them becomes dead work.
//
// zalo-oa's and dispact-connector's pollfailure.go are the same file for the same
// reason; the three connectors should read alike about the one thing they share.
// WHERE THIS UNIT DIFFERS is the delay, and pollRetryDelay says exactly how.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// fleetFailure classifies a tick in which EVERY capturing member failed.
//
// A SHARED class is reported as itself, and that is the case worth getting right:
// every member failing because Zalo does not answer from this installation is one
// outage happening in several places, and reporting it as its own class is what
// turns a screenful of dead jobs into a sentence naming the thing to go fix.
// Answering "everybody failed" there would throw away the one fact the tick
// actually established.
//
// Members failing for DIFFERENT reasons is the genuinely different situation, and
// the class for it says so rather than picking one member's cause to speak for the
// rest. Nothing is common to them, so there is no single outage to chase, and the
// remedy sends the operator to the per-member classes that do have specific
// answers.
func fleetFailure(ctx context.Context, failed []extension.FailureClass) error {
	cause := fmt.Errorf("zalo-personal: all %d connection(s) failed this tick", len(failed))
	shared := failed[0]
	for _, class := range failed[1:] {
		if class.Class != shared.Class {
			return extension.Failure(classEveryMemberFailed, cause)
		}
	}
	return dispositionFor(ctx, shared, cause)
}

// pollRetryDelay is how long a tick postpones itself for when NO member could
// reach Zalo. It is the DISPATCHER'S OWN CADENCE (api/jobs.yaml), and the match is
// the design rather than a coincidence.
//
// A postponed child sits in `scheduled`, which is one of the states the fan-out's
// uniqueness window covers, so while it waits the dispatcher's next insert for
// this workspace collapses into it. The postponement therefore REPLACES the tick
// it would have raced, and asking for the cadence is asking for exactly the rhythm
// the connector already has. Said exactly: the delay runs from the FAILURE rather
// than from the schedule, so during an outage the effective interval is the cadence
// plus however long a tick spends discovering it cannot reach anybody — strictly
// slower than health, never faster.
//
// THIS UNIT HAS A BACKOFF LADDER AND THIS IS NOT IT, which is the one thing not to
// carry over from the sibling connectors, neither of which has one. What spaces a
// member's drains is `poll_after` on their own connection row, geometric from
// baseDrainInterval and capped at maxPollBackoff (cadence.go) — and a FAILED turn
// deliberately touches neither that column nor the streak behind it. So the two
// numbers govern different things and cannot be reconciled into one: this one says
// when the TICK runs again, the ladder says when a MEMBER is next due, and
// postponing the tick moves nobody's place in the ladder. That is why the siblings'
// cadence argument transfers intact to a unit whose shape is not theirs.
//
// THE RETENTION INVARIANT IS UNTOUCHED, and it is the invariant this unit is sized
// against rather than a load preference. The worst gap between two drains of one
// member is maxPollBackoff + dispatcherCadence + jobTimeout; a postponement
// occupies the cadence term that sum ALREADY counts, because it collapses the
// insert that term is about rather than adding a wait in front of it.
//
// WHAT A POSTPONEMENT DOES NOT BUY, said plainly so nobody reads more into it: it
// does not make an outage cheaper in retention terms. While Zalo is unreachable
// nothing is drained by anybody, so whatever expires in its queue expires whether
// this tick postpones or dies. What changes is only whether an operator is shown a
// dead row every minute for the length of it, about work no human can help with.
//
// And maxPollBackoff equalling the seam's own 15-minute ceiling is a COINCIDENCE
// rather than a construction — nothing binds them. Nothing here relies on it:
// this delay is a minute and never approaches either bound.
const pollRetryDelay = 60 * time.Second

// dispositionFor decides whether a fleet-wide failure FAILS the tick or postpones
// it, which is a different question from what the failure is called.
//
// Zalo being unreachable by anybody is the one class whose own remedy says that
// nothing needs re-scanning — no cursor moved, so the next reachable tick re-reads
// the same backlog. Failing it anyway spends the child's three attempts and
// discards the row, and at this cadence an outage of any length therefore
// manufactures dead work every minute, each piece of it raising a banner that says
// a human must intervene in work no human can help with.
//
// Every other class still fails, INCLUDING the mixed one: members failing for
// different reasons is not an outage waiting to clear, it is several problems with
// several owners, and postponing it would be this unit quietly deciding not to tell
// any of them.
//
// A TICK WHOSE OWN CONTEXT IS DONE IS REFUSED, and this arm is the reason
// dispositionFor takes a context at all. The classification is still right —
// nothing was reached — but the disposition is not. A tick that ran out of wall
// clock did not meet an outage: it met its own window, because there is more work
// here than the window holds, and every later tick spends the same window and
// expires in the same place. THIS UNIT IS THE ONE MOST ABLE TO REACH THAT STATE,
// because a fleet of members each drains a socket until it goes quiet: a tick with
// more members than its 300s wall clock holds expires every time, and postponing
// that would hide a fan-out that can NEVER finish behind a row that looks like it
// is waiting patiently, with no dead work and no error column anywhere to say
// otherwise. It needs a wider timeout or fewer members per tick, and a human to
// choose. A CANCELLED context is a role shutting down, and postponing that delays
// the next drain by a whole cadence on every restart.
//
// The tick's context is asked rather than the cause, because the cause cannot
// answer precisely: unreachedTheProvider deliberately treats a per-member deadline
// as a transport failure, which is right for the CLASS — that member reached
// nobody — and says nothing about whether the TICK still has time. Asking the
// context separates our own clock running out from Zalo not answering, and only
// the first is a fact about this installation.
func dispositionFor(ctx context.Context, class extension.FailureClass, cause error) error {
	if class.Class == classProviderUnavailable.Class && ctx.Err() == nil {
		return extension.Reschedule(class, pollRetryDelay, cause)
	}
	return extension.Failure(class, cause)
}

// failureClass names what went wrong in THIS unit's vocabulary — the token a
// member's screen and the connection row carry, and the two sentences an operator
// reads, as one declared value (failureclasses.go). Zalo's own message is
// deliberately not carried: the class is rendered on a member's screen, and a
// remote party's prose is not this installation's to display or to publish.
func failureClass(cause error) extension.FailureClass {
	switch {
	case errors.Is(cause, extension.ErrForbidden):
		return classSessionWithdrawn
	case errors.Is(cause, extension.ErrInvalid):
		return classConnectionUnusable
	case unreachedTheProvider(cause):
		return classProviderUnavailable
	default:
		return classPollFailed
	}
}

// unreachedTheProvider reports that the request left this process and no answer
// came back — a timeout, a refused connection, a TLS handshake Zalo did not
// finish, or the per-member budget expiring mid-call.
//
// IT IS THE ONE PLACE THAT DECIDES, because three different answers hang off it:
// the class a member's screen shows, whether the row is parked for a human with a
// phone (pollMember), and whether a fleet-wide failure postpones the tick or
// becomes dead work (dispositionFor). A transport failure says nothing about the
// credential, so it may never do the second.
func unreachedTheProvider(cause error) bool {
	var unanswered *transportError
	return errors.As(cause, &unanswered) ||
		errors.Is(cause, errUnanswered) ||
		errors.Is(cause, context.DeadlineExceeded) ||
		errors.Is(cause, context.Canceled)
}
