// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

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
	sender, auth, granted, err := d.resolver.Resolve(ctx, del.UserID, del.Provider)
	switch {
	case errors.Is(err, ErrNoMailbox):
		return d.park(ctx, del.ID, "no mailbox is connected for this provider; connect one to enable sending")
	case errors.Is(err, ErrCannotSend):
		return d.park(ctx, del.ID, fmt.Sprintf("the %s connection cannot transmit messages", del.Provider))
	case err != nil:
		// Park only on an answer, never on a failure to get one.
		return d.retry(ctx, del.ID, err)
	}

	// Gate: authority. It refuses first so that a caller with no rights at
	// all learns nothing about the recipients' consent state.
	scope, sends := SendScopeFor(del.Provider)
	if !sends {
		return d.park(ctx, del.ID, fmt.Sprintf("provider %q cannot send messages", del.Provider))
	}
	if !slices.Contains(granted, scope) {
		return d.park(ctx, del.ID, "this mailbox connection was not granted the send scope; reconnect it to enable sending")
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

	return d.transmit(ctx, del, sender, auth)
}

// outcomeUndecided is the zero Outcome: a step that reached no verdict and
// leaves the delivery to the next one. It never leaves this package.
const outcomeUndecided Outcome = ""

// gateSeat refuses a delivery whose sender is no longer a live seat, and
// returns outcomeUndecided when they are.
//
// It PARKS rather than retries, because a deactivation is an answer: the
// account is off-boarded or compromised, and no amount of waiting restores the
// authority that staged this message. Retrying would keep the batch alive for
// the whole maximum age, which is the exposure this gate closes. A seat
// authority that could not ANSWER is the opposite case and retries, so an
// identity-store outage does not destroy every send in flight.
func (d *Dispatcher) gateSeat(ctx context.Context, del Delivery) (Outcome, time.Duration, error) {
	if d.seats == nil {
		// A send path with no seat authority wired is a deployment defect, and
		// this lane reaches a real external mailbox. Fail closed, exactly as
		// the missing consent authority below does.
		return d.park(ctx, del.ID, "no seat authority is configured on this send path")
	}
	active, err := d.seats.ActiveSeat(ctx, del.UserID)
	if err != nil {
		return d.retry(ctx, del.ID, err)
	}
	if !active {
		return d.park(ctx, del.ID,
			"the sender's account is no longer active; a deactivated user's mailbox may not transmit staged messages")
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
	// EVERY addressee is asked about, not just the To line: a Cc'd person is
	// owed the same suppression, and this call is the only one that runs after
	// they could have withdrawn.
	switch err := d.consent.RequireGrantedForEmails(ctx, addressees(del), del.ConsentPurpose); {
	case errors.Is(err, apperrors.ErrConsentNotGranted):
		// An answer: consent is absent, and no amount of waiting brings it
		// back.
		return d.park(ctx, del.ID, fmt.Sprintf(
			"consent for purpose %q is not granted for these recipients", del.ConsentPurpose))
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
				policy.Name(), age.Round(time.Second), d.maxAge))
		}
		return d.postpone(ctx, del.ID, "waiting: "+policy.Name(), wait)
	}
	return outcomeUndecided, 0, nil
}

// transmit hands the message to the provider and records what came back.
func (d *Dispatcher) transmit(ctx context.Context, del Delivery, sender connector.Sender, auth connector.Auth) (Outcome, time.Duration, error) {
	// Every staged field travels: a retry must rebuild an identical message,
	// and a field dropped here is a header silently missing from real mail.
	// Attempt counts the transmissions BEFORE this one — Load already counted
	// this attempt — so a first transmission arrives as 0 and the connector's
	// prior-send lookup runs only on a real retry.
	receipt, err := sender.Send(ctx, auth, connector.OutboundMessage{
		To: del.Recipients, Cc: del.Cc,
		Subject: del.Subject, Body: del.Body,
		MessageID:           del.MessageID,
		InReplyTo:           del.InReplyTo,
		References:          del.References,
		ListUnsubscribe:     del.ListUnsubscribe,
		ListUnsubscribePost: rfc8058Post(del.ListUnsubscribe),
		Attempt:             max(del.Attempts-1, 0),
	})
	if err != nil {
		return d.classifySendFailure(ctx, del, err)
	}

	// The provider has the message, so the mailbox's quota is spent whether
	// or not the receipt records cleanly. Policies that meter an actual
	// transmission are told here rather than at Wait: a limiter counting
	// checks instead of sends would pace nothing.
	for _, policy := range d.policies {
		if recorder, meters := policy.(SendRecorder); meters {
			recorder.Recorded(del)
		}
	}

	if err := d.store.RecordSent(ctx, del.ID, receipt.ProviderMessageID); err != nil {
		if errors.Is(err, ErrTerminal) {
			// A newer attempt already closed this row against its own
			// receipt; overwriting it would replace a real one.
			return OutcomeSkipped, 0, nil
		}
		return OutcomeRetry, 0, fmt.Errorf("comms: recording the send receipt: %w", err)
	}
	return OutcomeSent, 0, nil
}

// classifySendFailure turns a provider failure into a disposition using only
// the shared sentinel vocabulary, so the provider's own text stops at the
// connector boundary.
//
// There is deliberately no permanent-rejection branch. The Gmail connector
// maps every non-throttled, non-2xx response to ErrUnreachable, so a refused
// recipient is indistinguishable from an outage at this seam; a permanently
// rejected recipient therefore burns the whole retry ladder before its job
// exhausts and the delivery parks.
func (d *Dispatcher) classifySendFailure(ctx context.Context, del Delivery, err error) (Outcome, time.Duration, error) {
	if errors.Is(err, connector.ErrAuthRejected) {
		return d.park(ctx, del.ID, "the provider rejected this mailbox's credential; reconnect the mailbox to resume sending")
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
			"the provider is rate limiting this mailbox and the retry ladder is exhausted after %d attempts", del.Attempts))
	}
	if err := d.store.RecordFailure(ctx, del.ID, "waiting: the provider is rate limiting this mailbox"); err != nil {
		if errors.Is(err, ErrTerminal) {
			return OutcomeSkipped, 0, nil
		}
		return OutcomeRetry, 0, fmt.Errorf("comms: recording the provider throttle: %w", err)
	}
	return OutcomePostponed, retryAfter, nil
}

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

// maxFaultLen bounds what one fault contributes to a delivery's reason. The
// causes reaching retry include arbitrary infrastructure errors — a wrapped
// database error carries SQL text and table names — and the column they land
// in is unbounded text, so without a bound a single such error would put a
// kilobyte of internals on the row an operator reads.
const maxFaultLen = 200

// faultReason renders a fault as the kind of operator sentence every other
// reason in this file is, bounded and truncated on a RUNE boundary: a
// byte-offset cut can split a UTF-8 sequence, leaving a mangled tail on the
// one row that explains the failure.
func faultReason(cause error) string {
	msg := cause.Error()
	if len(msg) > maxFaultLen {
		cut := maxFaultLen
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + "…"
	}
	return "transient fault, will retry: " + msg
}

// retry records why this attempt failed and hands the cause back so the
// caller's ladder can back off. The delivery stays pending: this is a fault,
// not a verdict.
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
