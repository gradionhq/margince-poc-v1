// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// sendingResolver is the resolver every transmit-side test needs: a mailbox
// that resolves cleanly and holds the send scope, so the gates pass and the
// step under test is the one that decides the outcome.
func sendingResolver() fakeResolver {
	return fakeResolver{sender: &fakeSender{}, granted: []string{sendScope}}
}

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

	got, err := dispatch(context.Background(), d, store.delivery.ID)
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
	if store.deferred == "" {
		t.Error("deferred with no reason; an operator cannot tell which rule deferred it")
	}
	// The DEFERRAL transition, not the failure one: a deferral that noted a
	// failure would also spend a rung of the transmit ladder this dispatch
	// never used, and a paced mailbox would park as "exhausted" without ever
	// having reached the provider.
	if store.failed != "" {
		t.Errorf("a postponement recorded a failure (%q); it must record a deferral, which gives the attempt back", store.failed)
	}
}

// A permanently saturated policy must not defer forever.
func TestDispatchParksADeliveryThatHasAgedOutWhileWaiting(t *testing.T) {
	store := &fakeStore{delivery: liveDelivery()}
	store.delivery.CreatedAt = testNow.Add(-2 * time.Hour)
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{}, granted: []string{sendScope}}, stubConsent{},
		waitPolicy{d: time.Minute})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked {
		t.Errorf("outcome = %v, want OutcomeParked past the max age", got)
	}
	// Naming the elapsed window and the rule that held it is the whole point
	// of computing the age: without them an aged-out park tells an operator
	// only that a message did not go, not what stopped it or for how long.
	if !strings.Contains(store.parked, "2h0m0s") || !strings.Contains(store.parked, "test_wait") {
		t.Errorf("park reason %q names neither the elapsed window nor the policy that deferred it", store.parked)
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

	got, err := dispatch(context.Background(), d, store.delivery.ID)
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

	if got, _ := dispatch(context.Background(), d, store.delivery.ID); got != OutcomeParked {
		t.Errorf("outcome = %v, want OutcomeParked — a dead grant is not retryable", got)
	}
}

func TestDispatchRetriesWhenTheProviderIsUnreachable(t *testing.T) {
	sender := &fakeSender{err: connector.ErrUnreachable}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	if got, _ := dispatch(context.Background(), d, store.delivery.ID); got != OutcomeRetry {
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

// Load already counted this attempt, so Attempt is the count of transmissions
// BEFORE this one and the provider's prior-send lookup runs only on a real
// retry. Getting it too low mails a real recipient twice; too high suppresses
// a legitimate first send against a lookup that finds nothing.
func TestDispatchPassesTheRetryCountToTheSender(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attempts int
		want     int
	}{
		{"a retry reports the transmissions before it", 3, 2},
		// A row Load never counted must not produce a negative Attempt: the
		// floor is what keeps an unexpected zero from wrapping into a value
		// that would make a first send look like a retry.
		{"an uncounted attempt floors at zero", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeSender{}
			store := &fakeStore{delivery: liveDelivery()}
			store.delivery.Attempts = tc.attempts
			d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

			if _, err := dispatch(context.Background(), d, store.delivery.ID); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if sender.seen.Attempt != tc.want {
				t.Errorf("Attempt = %d, want %d", sender.seen.Attempt, tc.want)
			}
		})
	}
}

// Every staged field must reach the wire. A field dropped when the message is
// built is a header silently missing from real mail while every other test
// here still passes.
func TestDispatchTransmitsEveryStagedFieldOnTheWire(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

	if _, err := dispatch(context.Background(), d, store.delivery.ID); err != nil {
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
	// liveDelivery is on its first attempt (Load counted it), so the provider
	// must be told this is a first transmission and skip its prior-send
	// lookup. Anything above zero here suppresses a send that never happened.
	if got.Attempt != 0 {
		t.Errorf("Attempt = %d on a first transmission, want 0", got.Attempt)
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

	if _, err := dispatch(context.Background(), d, store.delivery.ID); err != nil {
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

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d, want OutcomeParked/0", got, sender.calls)
	}
	if store.parked == "" {
		t.Error("parked with no reason; an operator cannot act on that")
	}
}

// An unconfigured ladder length DEFAULTS; it does not switch the exhaustion
// guard off. Reading a non-positive bound as "zero attempts allowed" would
// park every delivery on its first attempt, and reading it as "no bound"
// would leave an exhausted row pending forever with nothing to move it —
// the silent version of the failure this guard exists to prevent. Both halves
// are pinned here: a delivery under the default still transmits, and one that
// reaches the default still parks.
func TestDispatchDefaultsAnUnconfiguredLadderBound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attempts int
		want     Outcome
		calls    int
	}{
		{"under the default bound", 1, OutcomeSent, 1},
		{"at the default bound", defaultMaxAttempts, OutcomeParked, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeSender{}
			store := &fakeStore{delivery: liveDelivery()}
			store.delivery.Attempts = tc.attempts
			d := NewDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{},
				nil, func() time.Time { return testNow }, time.Hour, 0)

			got, err := dispatch(context.Background(), d, store.delivery.ID)
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if got != tc.want || sender.calls != tc.calls {
				t.Errorf("outcome=%v calls=%d, want %v/%d", got, sender.calls, tc.want, tc.calls)
			}
		})
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

	if _, err := dispatch(context.Background(), d, store.delivery.ID); err != nil {
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

	got, err := dispatch(context.Background(), d, store.delivery.ID)
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

	got, err := dispatch(context.Background(), d, store.delivery.ID)
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

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSkipped {
		t.Errorf("outcome = %v, want OutcomeSkipped", got)
	}
}

// A transition that failed for a reason that is NOT ErrTerminal left the row
// exactly as it was: still pending, with no record that this attempt reached a
// disposition at all. Reporting the disposition anyway would claim a durable
// fact that was never written — a park nobody can see, or a receipt no row
// carries. The attempt goes back on the ladder with the fault instead.
func TestDispatchRetriesWhenATransitionFailsForANonTerminalReason(t *testing.T) {
	dbDown := errors.New("connection reset by peer")
	for _, tc := range []struct {
		name     string
		store    *fakeStore
		resolver fakeResolver
		policies []SendPolicy
	}{
		{"park", &fakeStore{delivery: liveDelivery(), parkErr: dbDown}, fakeResolver{err: ErrNoMailbox}, nil},
		{"postpone", &fakeStore{delivery: liveDelivery(), deferErr: dbDown}, sendingResolver(), []SendPolicy{waitPolicy{d: time.Minute}}},
		{"failure note", &fakeStore{delivery: liveDelivery(), failedErr: dbDown}, fakeResolver{err: errors.New("keyvault timeout")}, nil},
		{"send receipt", &fakeStore{delivery: liveDelivery(), sentErr: dbDown}, sendingResolver(), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDispatcher(tc.store, tc.resolver, stubConsent{}, tc.policies...)

			got, err := dispatch(context.Background(), d, tc.store.delivery.ID)
			if got != OutcomeRetry {
				t.Errorf("outcome = %v, want OutcomeRetry", got)
			}
			if err == nil {
				t.Error("no error returned; a caller's ladder cannot back off on a silent failure")
			}
		})
	}
}

// A fault's own text reaches the delivery's reason column, which is unbounded
// and operator-facing. An arbitrary infrastructure error — a wrapped database
// error carrying SQL and table names — must arrive bounded and labelled rather
// than raw, and truncation must land on a rune boundary or the row an operator
// reads ends in mojibake.
func TestDispatchBoundsAFaultBeforeWritingItToTheDeliveryReason(t *testing.T) {
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{err: errors.New(strings.Repeat("é", 500))}, stubConsent{})

	if got, _ := dispatch(context.Background(), d, store.delivery.ID); got != OutcomeRetry {
		t.Fatalf("outcome = %v, want OutcomeRetry", got)
	}
	if !strings.HasPrefix(store.failed, "transient fault, will retry: ") {
		t.Errorf("reason %q is not labelled as a transient fault", store.failed)
	}
	if len(store.failed) > maxFaultLen+len("transient fault, will retry: ")+len("…") {
		t.Errorf("reason is %d bytes; the fault was not bounded", len(store.failed))
	}
	if !utf8.ValidString(store.failed) {
		t.Errorf("reason %q is not valid UTF-8; truncation split a rune", store.failed)
	}
}
