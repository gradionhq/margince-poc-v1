// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// Outcome is what one dispatch attempt concluded. It is the caller's whole
// instruction: a job runner maps it to "done", "snooze", or "back off" without
// re-deriving anything from the delivery row.
type Outcome string

const (
	// OutcomeSent means the provider has the message and the receipt is recorded.
	OutcomeSent Outcome = "sent"
	// OutcomeSkipped means there was nothing left to do — the delivery was
	// already terminal when this attempt reached it.
	OutcomeSkipped Outcome = "skipped"
	// OutcomePostponed means the delivery may still go, but not yet; the
	// returned wait is how long the caller should defer it.
	OutcomePostponed Outcome = "postponed"
	// OutcomeParked means the delivery will never go and the row says why.
	OutcomeParked Outcome = "parked"
	// OutcomeRetry means the attempt failed to reach a verdict; the delivery
	// stays pending for the caller's retry ladder.
	OutcomeRetry Outcome = "retry"
)

// Dispatcher runs one delivery attempt: the fixed gates that can refuse it, the
// configurable policy chain that can postpone it, and the transmission itself.
//
// Gates and policies are deliberately different mechanisms because they are
// different facts. A gate says NEVER — no amount of waiting repairs a revoked
// grant or a withdrawn consent — so gates are inline, fixed, and not
// configurable. A policy says NOT YET, so policies are an ordered chain the
// deployment assembles.
type Dispatcher struct {
	store       deliveryStore
	resolver    ConnectionResolver
	seats       SeatAuthority
	consent     ConsentGate
	policies    []SendPolicy
	now         func() time.Time
	maxAge      time.Duration
	maxAttempts int
}

// defaultMaxAttempts bounds a dispatcher whose ladder length was not
// configured. A missing bound must still park eventually: once the runner
// stops delivering an exhausted job nothing else moves the row off pending,
// and a row that looks live forever is the failure the exhaustion guard exists
// to prevent. Disabling the guard on a non-positive bound would trade a loud
// catastrophe for a silent one, so it defaults rather than disappearing.
//
// The value is a generous finite ceiling, not a claim about any particular
// runner's ladder — a caller that knows its own should pass it.
const defaultMaxAttempts = 25

// minMaxAttempts is the floor a configured ladder length is raised to. One
// rung is arithmetically positive and survives the default above, but Load
// counts an attempt BEFORE the exhaustion guard reads the counter, so a bound
// of one would meet `Attempts >= maxAttempts` on the very first dispatch and
// park every delivery without ever asking a provider. Two rungs is the
// smallest bound under which the guard bounds a ladder rather than replacing
// it.
const minMaxAttempts = 2

// NewDispatcher builds the dispatcher. maxAge bounds how long a delivery may
// be postponed before it parks instead, and maxAttempts is the caller's retry
// ladder length — the dispatcher parks on the last rung rather than leaving a
// row the runner will never deliver again looking pending forever.
//
// Neither knob can be set to the absence of the behaviour it configures: a nil
// clock DEFAULTS to time.Now, and maxAttempts defaults to defaultMaxAttempts
// when unset and is floored at minMaxAttempts when below it. A caller that
// forgets one gets the conservative version of the rule, never no rule.
func NewDispatcher(
	store deliveryStore,
	resolver ConnectionResolver,
	seats SeatAuthority,
	consent ConsentGate,
	policies []SendPolicy,
	now func() time.Time,
	maxAge time.Duration,
	maxAttempts int,
) *Dispatcher {
	if now == nil {
		now = time.Now
	}
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	if maxAttempts < minMaxAttempts {
		maxAttempts = minMaxAttempts
	}
	return &Dispatcher{
		store: store, resolver: resolver, seats: seats, consent: consent, policies: policies,
		now: now, maxAge: maxAge, maxAttempts: maxAttempts,
	}
}

// DispatchWithWait runs one delivery attempt and reports how long to wait when
// the outcome is OutcomePostponed (zero for every other outcome).
//
// The sequence is authority → consent → pacing, and the order is load-bearing
// rather than stylistic: authority must refuse BEFORE consent answers, or the
// difference between "you may not" and "they said no" tells a caller with no
// rights at all something about a person's consent state.
func (d *Dispatcher) DispatchWithWait(ctx context.Context, id ids.UUID) (Outcome, time.Duration, error) {
	// Load counts this attempt and refuses a delivery that already finished.
	// Job delivery is at-least-once, and that terminal status — not any
	// in-flight claim, of which there is none by design — is what makes a
	// redelivery safe: a redelivered job stops here instead of mailing a
	// second copy.
	del, err := d.store.Load(ctx, id)
	if errors.Is(err, ErrTerminal) {
		return OutcomeSkipped, 0, nil
	}
	if err != nil {
		// A load that failed to answer is an outage, not a verdict, and
		// there is no row in hand to record a reason against.
		return OutcomeRetry, 0, err
	}

	// Resolve first, because the authority gate reads the scopes the provider
	// says this grant holds right now — not a copy stored when it was granted.
	// resolveSeam is the ONE branch on provider class (sendseam.go); everything
	// from here down is one path for both transports.
	seam, err := d.resolveSeam(ctx, del)
	switch {
	case errors.Is(err, ErrNoMailbox):
		return d.park(ctx, del.ID, fmt.Sprintf(
			"nothing is connected for %s to transmit through; connect it to enable sending", del.Provider))
	case errors.Is(err, ErrCannotSend):
		return d.park(ctx, del.ID, fmt.Sprintf("the %s connection cannot transmit messages", del.Provider))
	case errors.Is(err, ErrProviderNotConfigured):
		return d.park(ctx, del.ID, fmt.Sprintf(
			"this installation has no %s integration configured to transmit through; configure it, then re-send", del.Provider))
	case err != nil:
		// Park only on an answer, never on a failure to get one.
		return d.retry(ctx, del.ID, err)
	}

	// Gate: authority. It refuses first so that a caller with no rights at
	// all learns nothing about the recipients' consent state.
	if outcome, wait, err := d.gateSendAuthority(ctx, del, seam.granted); outcome != outcomeUndecided {
		return outcome, wait, err
	}

	// Gate: the sender's seat, which is authority-class and therefore belongs
	// here rather than after consent. The mailbox grant above is the
	// PROVIDER's answer about a credential; this is THIS installation's answer
	// about the human it was lent by, and deactivating them touches neither
	// the connection nor the grant.
	if outcome, wait, err := d.gateSeat(ctx, del); outcome != outcomeUndecided {
		return outcome, wait, err
	}

	// Gate: suppression and consent, which are one step — one-click
	// unsubscribe writes a per-purpose consent withdrawal, so this gate IS
	// the suppression mechanism.
	if outcome, wait, err := d.gateConsent(ctx, del); outcome != outcomeUndecided {
		return outcome, wait, err
	}

	// Policies postpone; they never refuse. They run after both gates, so a
	// delivery that may never go is refused rather than paced.
	if outcome, wait, err := d.pace(ctx, del); outcome != outcomeUndecided {
		return outcome, wait, err
	}

	// Ladder exhaustion. Once the runner stops delivering this job nothing
	// else would ever move the row off pending, and it would look live
	// forever. NewDispatcher floors the bound at minMaxAttempts, which is what
	// keeps this from parking a delivery on its first attempt — Load counts
	// the attempt before the comparison reads it.
	if del.Attempts >= d.maxAttempts {
		return d.park(ctx, del.ID, fmt.Sprintf("the retry ladder is exhausted after %d attempts", del.Attempts))
	}

	return d.transmit(ctx, del, seam)
}

// gateSendAuthority refuses a delivery this installation's own knowledge of the
// provider says can never leave, and returns outcomeUndecided when it may.
//
// It reads the PROVIDER's answer about a credential — granted is the scope list
// the resolver just read from the provider, not a copy stored when the grant was
// made — and it applies the scope check only where the provider HAS a scope to
// check. A credential carrying no OAuth grant is its own authority: the resolver
// either produced one or reported that it could not, so demanding a scope of it
// would park every message the provider can actually send, with a reason naming
// a connector limitation that does not exist.
//
// Both refusals PARK. Neither a provider this installation cannot transmit
// through nor a connection the provider never granted the send scope is repaired
// by waiting; the scope one names reconnecting, which is the act that repairs it.
func (d *Dispatcher) gateSendAuthority(ctx context.Context, del Delivery, granted []string) (Outcome, time.Duration, error) {
	switch scope, capability := SendScopeFor(del.Provider); capability {
	case CannotSend:
		return d.park(ctx, del.ID, fmt.Sprintf("provider %q cannot send messages", del.Provider))
	case SendsWithScope:
		if !slices.Contains(granted, scope) {
			return d.park(ctx, del.ID, "this mailbox connection was not granted the send scope; reconnect it to enable sending")
		}
	case SendsWithoutScope:
		// Nothing to intersect: the resolved credential is the whole authority,
		// and the seat gate is what still binds the human who lent it.
	}
	return outcomeUndecided, 0, nil
}

// gateSeat refuses a delivery whose sender is no longer a live,
// mutation-capable seat, and returns outcomeUndecided when they are.
//
// It PARKS rather than retries, because both an off-boarding and a downgrade
// to a read seat are answers: the authority that staged this message is gone
// either way, and no amount of waiting restores it. Retrying would keep the
// batch alive for the whole maximum age, which is the exposure this gate
// closes. A seat authority that could not ANSWER is the opposite case and
// retries, so an identity-store outage does not destroy every send in flight.
func (d *Dispatcher) gateSeat(ctx context.Context, del Delivery) (Outcome, time.Duration, error) {
	if d.seats == nil {
		// A send path with no seat authority wired is a deployment defect, and
		// this lane reaches a real external mailbox. Fail closed, exactly as
		// the missing consent authority below does.
		return d.park(ctx, del.ID, "no seat authority is configured on this send path")
	}
	active, reason, err := d.seats.ActiveSeat(ctx, del.UserID)
	if err != nil {
		return d.retry(ctx, del.ID, err)
	}
	if !active {
		return d.park(ctx, del.ID, reason)
	}
	return outcomeUndecided, 0, nil
}

// gateConsent asks the authoritative suppression question and returns
// outcomeUndecided when every addressee may still be mailed.
func (d *Dispatcher) gateConsent(ctx context.Context, del Delivery) (Outcome, time.Duration, error) {
	if d.consent == nil {
		// A send path with no consent authority wired is a deployment defect.
		// Retrying would hide the misconfiguration behind a delivery that
		// quietly never goes out.
		return d.park(ctx, del.ID, "no consent authority is configured on this send path")
	}
	// EVERY subject this delivery reaches is asked about, not just the To line:
	// a Cc'd person is owed the same suppression, and this call is the only one
	// that runs after they could have withdrawn. consentRecipients is what makes
	// the question shape-agnostic — mail's addressees and a channel's single
	// recipient arrive here as the same list.
	switch err := d.consent.RequireGrantedForRecipients(ctx, consentRecipients(del), del.ConsentPurpose); {
	case errors.Is(err, apperrors.ErrConsentNotGranted):
		// An answer: consent is absent, and no amount of waiting brings it
		// back.
		return d.park(ctx, del.ID, fmt.Sprintf(
			"consent for purpose %q is not granted for these recipients", del.ConsentPurpose,
		))
	case err != nil:
		// NOT an answer. A consent service that is merely down must not
		// permanently destroy a consented send — getting this branch backwards
		// silently kills legitimate mail.
		return d.retry(ctx, del.ID, err)
	}
	return outcomeUndecided, 0, nil
}

// pace applies the policy chain. The chain is ordered and the first non-zero
// wait wins, so adding a policy is a registration rather than a change to the
// dispatch sequence. It returns outcomeUndecided when every policy permits the
// delivery to go now.
func (d *Dispatcher) pace(ctx context.Context, del Delivery) (Outcome, time.Duration, error) {
	for _, policy := range d.policies {
		wait := policy.Wait(ctx, del)
		if wait <= 0 {
			continue
		}
		// A permanently saturated policy would defer this delivery forever,
		// silently — which looks fine right up until someone's email never
		// went out. Past the maximum age it parks with a reason instead.
		if age := d.now().Sub(del.CreatedAt); age > d.maxAge {
			return d.park(ctx, del.ID, fmt.Sprintf(
				"policy %q deferred this delivery for %s, past the %s maximum age",
				policy.Name(), age.Round(time.Second), d.maxAge,
			))
		}
		return d.postpone(ctx, del.ID, "waiting: "+policy.Name(), wait)
	}
	return outcomeUndecided, 0, nil
}

// transmit hands the message to the provider and records what came back. The
// seam already carries the shape-specific half (sendseam.go), so what follows is
// the same for a mail message and a channel one.
func (d *Dispatcher) transmit(ctx context.Context, del Delivery, seam sendSeam) (Outcome, time.Duration, error) {
	// At-most-once, for the seams that need it: a transmission whose outcome was
	// never learned is never attempted a second time.
	if outcome, wait, err := d.guardAtMostOnce(ctx, del, seam); outcome != outcomeUndecided {
		return outcome, wait, err
	}
	receipt, err := seam.transmit(ctx)
	if err != nil {
		return d.classifySendFailure(ctx, del, err)
	}

	if err := d.store.RecordSent(ctx, del.ID, receipt); err != nil {
		if errors.Is(err, ErrTerminal) {
			// A newer attempt already closed this row against its own
			// receipt; overwriting it would replace a real one.
			return OutcomeSkipped, 0, nil
		}
		return OutcomeRetry, 0, fmt.Errorf("comms: recording the send receipt: %w", err)
	}

	// Metering follows the DURABLE record, not the provider call, because the
	// send call is not the countable event: a receipt that failed to record
	// comes back on the ladder, and Send answers a retry from Gmail's
	// prior-send lookup rather than transmitting again. Metering at the call
	// would count that one message twice. RecordSent is guarded on
	// status = 'pending' and reports ErrTerminal otherwise, so exactly one
	// attempt per delivery ever reaches this line — which is what makes
	// "metered" mean "one message, once".
	//
	// Policies are told here rather than at Wait for the same reason in the
	// other direction: a limiter counting checks instead of sends paces
	// nothing.
	for _, policy := range d.policies {
		if recorder, meters := policy.(SendRecorder); meters {
			recorder.Recorded(del)
		}
	}
	return OutcomeSent, 0, nil
}

// classifySendFailure turns a provider failure into a disposition using only
// the shared sentinel vocabulary, so the provider's own text stops at the
// connector boundary.
//
// A permanent rejection is recognized only where the SEAM can prove it:
// ErrRecipientUnreachable is reported by a provider that answers a refused
// recipient differently from a refused credential, and it parks at once. The
// Gmail connector cannot — it maps every non-throttled, non-2xx response to
// ErrUnreachable, so a refused mail recipient is indistinguishable from an
// outage there and still burns the whole retry ladder before its job exhausts
// and the delivery parks.
func (d *Dispatcher) classifySendFailure(ctx context.Context, del Delivery, err error) (Outcome, time.Duration, error) {
	if errors.Is(err, connector.ErrSendOutcomeUnknown) {
		// NEVER retried, and no shape test is needed to decide that: only a seam
		// that cannot discover a prior send reports this class, and one that can
		// is obliged to go and find out instead. The in-flight marker
		// deliberately STAYS — it is the durable record that a message may
		// already be with the customer, and the park reason is the only honest
		// thing to tell the operator reading the row.
		return d.park(ctx, del.ID, unknownOutcomeReason)
	}
	// Everything below is a DEFINITE answer from the provider, which proves
	// nothing was transmitted — so the in-flight marker is retracted before the
	// delivery goes back on the ladder. It is a no-op for a seam that never set
	// one, which is what keeps this a single rule rather than a second branch on
	// provider class.
	if clearErr := d.store.ClearInFlight(ctx, del.ID); clearErr != nil && !errors.Is(clearErr, ErrTerminal) {
		// The marker is still standing, so the next attempt will park rather
		// than re-send. Both causes go back for the job log, and the delivery
		// errs toward an unsent message — the direction this whole path is built
		// to err in.
		return d.retry(ctx, del.ID, errors.Join(err, clearErr))
	}
	if errors.Is(err, connector.ErrAuthRejected) {
		return d.park(ctx, del.ID, "the provider rejected the credential this delivery transmits through; reconnect it to resume sending")
	}
	// Checked alongside the credential class, not after the ladder: the two are
	// the pair an operator most easily confuses, and the whole value of telling
	// them apart is that each row says which one it was.
	if errors.Is(err, connector.ErrRecipientUnreachable) {
		return d.park(ctx, del.ID, unreachableRecipientReason)
	}
	// Honour the provider's own interval when it named one: it knows when it
	// will accept the next message, and guessing shorter earns another
	// throttle. A rate limit with no stated interval leaves nothing to
	// honour, so it falls through to the retry ladder rather than asking the
	// caller to re-run immediately against a provider already throttling us.
	if limited, throttled := errors.AsType[*connector.RateLimitedError](err); throttled && limited.RetryAfter > 0 {
		return d.throttled(ctx, del, limited.RetryAfter)
	}
	return d.retry(ctx, del.ID, err)
}
