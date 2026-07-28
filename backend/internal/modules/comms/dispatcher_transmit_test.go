// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The postpone-and-transmit half of the dispatcher's spec: the policy chain,
// the two bounds that keep a deferred delivery from living forever, the
// message put on the wire, and how a provider failure is classified. The
// gates that refuse a send, and the shared harness both halves use, are in
// dispatcher_test.go.

type waitPolicy struct{ d time.Duration }

func (waitPolicy) Name() string                                   { return "test_wait" }
func (w waitPolicy) Wait(context.Context, Delivery) time.Duration { return w.d }

// recordingPolicy is a policy that meters an actual transmission, so it
// implements the optional SendRecorder seam. waitPolicy deliberately does not:
// a chain holding both proves the dispatcher type-asserts rather than assuming.
type recordingPolicy struct {
	d        time.Duration
	recorded int
}

func (*recordingPolicy) Name() string                                   { return "test_recording" }
func (p *recordingPolicy) Wait(context.Context, Delivery) time.Duration { return p.d }
func (p *recordingPolicy) Recorded(Delivery)                            { p.recorded++ }

func TestDispatchTransmitsAndRecordsTheReceipt(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	got, err := d.Dispatch(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSent || store.sent != "gmsg1" {
		t.Errorf("outcome=%v sent=%q, want OutcomeSent/gmsg1", got, store.sent)
	}
}

func TestDispatchSnoozesWhenAPolicyAsksToWait(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{},
		waitPolicy{d: 90 * time.Second})

	got, wait, err := d.DispatchWithWait(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomePostponed || wait != 90*time.Second || sender.calls != 0 {
		t.Errorf("outcome=%v wait=%v calls=%d, want OutcomePostponed/90s/0", got, wait, sender.calls)
	}
	if store.failed == "" {
		t.Error("deferred with no reason; an operator cannot tell which rule deferred it")
	}
}

// A permanently saturated policy must not defer forever.
func TestDispatchParksADeliveryThatHasAgedOutWhileWaiting(t *testing.T) {
	store := &fakeStore{delivery: liveDelivery()}
	store.delivery.CreatedAt = testNow.Add(-2 * time.Hour)
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{}, granted: []string{sendScope}}, stubConsent{},
		waitPolicy{d: time.Minute})

	got, _ := d.Dispatch(context.Background(), store.delivery.ID)
	if got != OutcomeParked {
		t.Errorf("outcome = %v, want OutcomeParked past the max age", got)
	}
}

// The age bound belongs to the postpone path only: a delivery that is old but
// that no policy is deferring has nothing to starve on, and parking it would
// destroy a send that was merely slow to reach the worker.
func TestDispatchTransmitsAnAgedDeliveryThatNoPolicyIsDeferring(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	store.delivery.CreatedAt = testNow.Add(-2 * time.Hour)
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	got, err := d.Dispatch(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSent || sender.calls != 1 {
		t.Errorf("outcome=%v calls=%d, want OutcomeSent/1", got, sender.calls)
	}
}

func TestDispatchParksOnARejectedGrant(t *testing.T) {
	sender := &fakeSender{err: connector.ErrAuthRejected}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	if got, _ := d.Dispatch(context.Background(), store.delivery.ID); got != OutcomeParked {
		t.Errorf("outcome = %v, want OutcomeParked — a dead grant is not retryable", got)
	}
}

func TestDispatchRetriesWhenTheProviderIsUnreachable(t *testing.T) {
	sender := &fakeSender{err: connector.ErrUnreachable}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	if got, _ := d.Dispatch(context.Background(), store.delivery.ID); got != OutcomeRetry {
		t.Errorf("outcome = %v, want OutcomeRetry", got)
	}
}

// Reachable via the provider's dedicated throttling case; honour the interval
// it stated rather than guessing a shorter one and earning another throttle.
func TestDispatchPostponesForTheProviderStatedRetryAfter(t *testing.T) {
	sender := &fakeSender{err: &connector.RateLimitedError{RetryAfter: 42 * time.Second}}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	got, wait, err := d.DispatchWithWait(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomePostponed || wait != 42*time.Second {
		t.Errorf("outcome=%v wait=%v, want OutcomePostponed/42s", got, wait)
	}
}

// A rate limit the provider named no interval for leaves nothing to honour.
// Postponing for zero would ask the caller to re-run immediately, spinning
// against a provider that is already throttling; the retry ladder is the
// honest fallback the port's contract names.
func TestDispatchRetriesOnARateLimitWithNoStatedInterval(t *testing.T) {
	sender := &fakeSender{err: &connector.RateLimitedError{}}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	got, wait, _ := d.DispatchWithWait(context.Background(), store.delivery.ID)
	if got != OutcomeRetry || wait != 0 {
		t.Errorf("outcome=%v wait=%v, want OutcomeRetry/0", got, wait)
	}
}

// Load already counted this attempt, so a first transmission arrives as
// Attempt=0 and the provider's prior-send lookup runs only on a real retry.
func TestDispatchPassesTheRetryCountToTheSender(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	store.delivery.Attempts = 3
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	if _, err := d.Dispatch(context.Background(), store.delivery.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if sender.seen.Attempt != 2 {
		t.Errorf("Attempt = %d, want 2", sender.seen.Attempt)
	}
}

// Every staged field must reach the wire. A field dropped when the message is
// built is a header silently missing from real mail while every other test
// here still passes.
func TestDispatchTransmitsEveryStagedFieldOnTheWire(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	if _, err := d.Dispatch(context.Background(), store.delivery.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	want, got := store.delivery, sender.seen
	if !slices.Equal(got.To, want.Recipients) {
		t.Errorf("To = %v, want %v", got.To, want.Recipients)
	}
	if !slices.Equal(got.Cc, want.Cc) {
		t.Errorf("Cc = %v, want %v", got.Cc, want.Cc)
	}
	if !slices.Equal(got.References, want.References) {
		t.Errorf("References = %v, want %v", got.References, want.References)
	}
	if got.Subject != want.Subject || got.Body != want.Body {
		t.Errorf("Subject=%q Body=%q, want %q/%q", got.Subject, got.Body, want.Subject, want.Body)
	}
	if got.MessageID != want.MessageID || got.InReplyTo != want.InReplyTo {
		t.Errorf("MessageID=%q InReplyTo=%q, want %q/%q", got.MessageID, got.InReplyTo, want.MessageID, want.InReplyTo)
	}
	if got.ListUnsubscribe != want.ListUnsubscribe {
		t.Errorf("ListUnsubscribe = %q, want %q", got.ListUnsubscribe, want.ListUnsubscribe)
	}
	if got.ListUnsubscribePost != "List-Unsubscribe=One-Click" {
		t.Errorf("ListUnsubscribePost = %q, want the RFC 8058 one-click literal", got.ListUnsubscribePost)
	}
}

// The one-click Post header is meaningless without a target: a mail client
// told to POST nowhere is worse than no header at all. Deriving it from its
// partner is what keeps the pair from drifting.
func TestDispatchDerivesNoUnsubscribePostWhenThereIsNothingToUnsubscribeFrom(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	store.delivery.ListUnsubscribe = ""
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	if _, err := d.Dispatch(context.Background(), store.delivery.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if sender.seen.ListUnsubscribePost != "" {
		t.Errorf("ListUnsubscribePost = %q with no List-Unsubscribe target", sender.seen.ListUnsubscribePost)
	}
}

// River stops delivering an exhausted job, and nothing else would ever move
// the row off pending; it would look live forever.
func TestDispatchParksOnTheFinalAttemptRatherThanLeavingItPending(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	store.delivery.Attempts = testMaxAttempts
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	got, _ := d.Dispatch(context.Background(), store.delivery.ID)
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d, want OutcomeParked/0", got, sender.calls)
	}
	if store.parked == "" {
		t.Error("parked with no reason; an operator cannot act on that")
	}
}

// A non-positive bound is no bound. Reading it as "zero attempts allowed"
// would park every delivery on its first attempt — silently destroying all
// outbound mail on a dispatcher whose ladder length was simply not configured.
func TestDispatchWithNoConfiguredLadderBoundStillTransmits(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := NewDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{},
		nil, func() time.Time { return testNow }, time.Hour, 0)

	got, err := d.Dispatch(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSent || sender.calls != 1 {
		t.Errorf("outcome=%v calls=%d, want OutcomeSent/1", got, sender.calls)
	}
}

// The limiter counts messages the provider actually received, so the
// dispatcher tells the chain only once transmission succeeds. Without this
// call the limiter never counts and the policy paces nothing.
func TestDispatchCountsASuccessfulSendAgainstEveryMeteringPolicy(t *testing.T) {
	meter := &recordingPolicy{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{}, granted: []string{sendScope}}, stubConsent{},
		waitPolicy{}, meter)

	if _, err := d.Dispatch(context.Background(), store.delivery.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if meter.recorded != 1 {
		t.Errorf("Recorded called %d times, want 1 — a limiter that counts checks paces nothing", meter.recorded)
	}
}

// A message that never reached the provider consumed none of the mailbox's
// quota. Counting a deferral would shrink the window every time the chain was
// merely consulted.
func TestDispatchCountsNothingAgainstAPolicyWhenTheSendIsPostponed(t *testing.T) {
	meter := &recordingPolicy{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{}, granted: []string{sendScope}}, stubConsent{},
		waitPolicy{d: 90 * time.Second}, meter)

	got, _, err := d.DispatchWithWait(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomePostponed {
		t.Fatalf("outcome = %v, want OutcomePostponed", got)
	}
	if meter.recorded != 0 {
		t.Errorf("Recorded called %d times for a message that never left", meter.recorded)
	}
}

// The three status-guarded transitions all report ErrTerminal when they touch
// zero rows, which means a newer attempt already closed this delivery. That is
// a benign no-op: turning it into a retry would put a finished delivery back
// on the ladder, and turning it into an error would fail a job that did its
// work correctly.

func TestDispatchTreatsATerminalRecordSentAsAlreadyHandled(t *testing.T) {
	store := &fakeStore{delivery: liveDelivery(), sentErr: ErrTerminal}
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{}, granted: []string{sendScope}}, stubConsent{})

	got, err := d.Dispatch(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSkipped {
		t.Errorf("outcome = %v, want OutcomeSkipped", got)
	}
}

func TestDispatchTreatsATerminalParkAsAlreadyHandled(t *testing.T) {
	store := &fakeStore{delivery: liveDelivery(), parkErr: ErrTerminal}
	d := newTestDispatcher(store, fakeResolver{err: ErrNoMailbox}, stubConsent{})

	got, err := d.Dispatch(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSkipped {
		t.Errorf("outcome = %v, want OutcomeSkipped", got)
	}
}

func TestDispatchTreatsATerminalFailureNoteAsAlreadyHandled(t *testing.T) {
	store := &fakeStore{delivery: liveDelivery(), failedErr: ErrTerminal}
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{err: connector.ErrUnreachable}, granted: []string{sendScope}}, stubConsent{})

	got, err := d.Dispatch(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSkipped {
		t.Errorf("outcome = %v, want OutcomeSkipped", got)
	}
}
