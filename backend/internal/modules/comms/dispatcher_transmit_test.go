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
// gates that refuse a send, and the shared harness all three files use, are in
// dispatcher_test.go; what an attempt RECORDS — the metering and the reason
// sentence — is in dispatcher_reason_test.go.

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
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSent || store.sent != "gmsg1" {
		t.Errorf("outcome=%v sent=%q, want OutcomeSent/gmsg1", got, store.sent)
	}
	// The WHOLE receipt reaches the store, not just the provider's own id: the
	// RFC822 identity is what the timeline row has to be re-keyed onto, and a
	// dispatcher that dropped it here would leave every sent message filed
	// under an identity no reply will ever quote.
	if store.stamped != "stamped@mail.gmail.com" {
		t.Errorf("stamped identity recorded = %q, want the one the provider reported", store.stamped)
	}
}

func TestDispatchSnoozesWhenAPolicyAsksToWait(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{},
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
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{}, granted: []string{sendScope}}, &stubConsent{},
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
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

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
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

	if got, _ := dispatch(context.Background(), d, store.delivery.ID); got != OutcomeParked {
		t.Errorf("outcome = %v, want OutcomeParked — a dead grant is not retryable", got)
	}
	// The credential park is the one that asks for a reconnection, and it must be
	// the ONLY one: the reason is the whole instruction an operator acts on.
	if !strings.Contains(store.parked, "reconnect") {
		t.Errorf("park reason %q does not name the reconnection that repairs a rejected credential", store.parked)
	}
}

// A recipient the provider permanently refuses parks at once, and the reason must
// name the RECIPIENT. Sharing the credential park's wording would send an operator
// to rotate a working credential, and falling through to the retry ladder would
// spend every attempt on a chat that will never accept the message and then park
// under a reason that names no cause at all.
func TestDispatchParksNamingTheRecipientWhenTheProviderRefusesToDeliverToThem(t *testing.T) {
	sender := &fakeSender{err: connector.ErrRecipientUnreachable}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked {
		t.Fatalf("outcome = %v, want OutcomeParked — no retry reaches a recipient the provider refuses", got)
	}
	if !strings.Contains(store.parked, "recipient") || !strings.Contains(store.parked, "blocked") {
		t.Errorf("park reason %q does not tell the operator that the recipient is the cause", store.parked)
	}
	// The credential wording is what this park must NOT reuse: rotating a
	// working token repairs nothing when the recipient is the cause.
	if strings.Contains(store.parked, "reconnect it to resume sending") {
		t.Errorf("park reason %q reuses the credential wording, which misdirects the operator", store.parked)
	}
	// A definite answer proves nothing was transmitted, so the marker that would
	// make the NEXT attempt park as an unknown outcome has to be retracted.
	if store.cleared != 1 {
		t.Errorf("the in-flight marker was cleared %d time(s), want 1 — the provider gave a definite answer", store.cleared)
	}
}

func TestDispatchRetriesWhenTheProviderIsUnreachable(t *testing.T) {
	sender := &fakeSender{err: connector.ErrUnreachable}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

	if got, _ := dispatch(context.Background(), d, store.delivery.ID); got != OutcomeRetry {
		t.Errorf("outcome = %v, want OutcomeRetry", got)
	}
}

// Reachable via the provider's dedicated throttling case; honour the interval
// it stated rather than guessing a shorter one and earning another throttle.
//
// It KEEPS its rung, unlike a pacing deferral: the message reached the provider
// and a 429 is the provider's answer, so it is a transmission attempt and the
// ladder has to bound it. Restoring the rung here would leave a throttled
// delivery snoozing forever — the rate policy meters only successful sends, so
// pace never reaches its own age park, and the caller's ladder is restored by
// the snooze this very call asks for.
func TestDispatchPostponesForTheProviderStatedRetryAfterAndKeepsItsRung(t *testing.T) {
	sender := &fakeSender{err: &connector.RateLimitedError{RetryAfter: 42 * time.Second}}
	store := &fakeStore{delivery: liveDelivery()}
	before := store.delivery.Attempts
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

	got, wait, err := d.DispatchWithWait(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomePostponed || wait != 42*time.Second {
		t.Errorf("outcome=%v wait=%v, want OutcomePostponed/42s", got, wait)
	}
	if store.deferred != "" {
		t.Errorf("a provider throttle was recorded as a pacing deferral (%q), which gives back a rung the message already spent at the provider", store.deferred)
	}
	if store.delivery.Attempts != before {
		t.Errorf("attempts went %d → %d on a provider throttle; the rung must stand", before, store.delivery.Attempts)
	}
	if !strings.Contains(store.failed, "rate limiting") {
		t.Errorf("reason %q does not tell an operator the PROVIDER throttled us rather than our own limiter", store.failed)
	}
}

// On the last rung there is no further attempt to honour the provider's
// interval on, so the delivery parks THERE — naming the throttle — rather than
// asking to be retried against a ladder that is already spent.
func TestDispatchParksOnAThrottleThatExhaustsTheLadder(t *testing.T) {
	sender := &fakeSender{err: &connector.RateLimitedError{RetryAfter: 42 * time.Second}}
	store := &fakeStore{delivery: liveDelivery()}
	store.delivery.Attempts = testMaxAttempts - 1
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

	got, wait, err := d.DispatchWithWait(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeParked || wait != 0 {
		t.Fatalf("outcome=%v wait=%v on the last rung, want OutcomeParked/0", got, wait)
	}
	if !strings.Contains(store.parked, "rate limiting") {
		t.Errorf("park reason %q does not name the provider throttle that stopped the message", store.parked)
	}
}

// A rate limit the provider named no interval for leaves nothing to honour.
// Postponing for zero would ask the caller to re-run immediately, spinning
// against a provider that is already throttling; the retry ladder is the
// honest fallback the port's contract names.
func TestDispatchRetriesOnARateLimitWithNoStatedInterval(t *testing.T) {
	sender := &fakeSender{err: &connector.RateLimitedError{}}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

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
			d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

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
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

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
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

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
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

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
			d := NewDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}},
				liveSeat(), nil, &stubConsent{}, nil, func() time.Time { return testNow }, time.Hour, 0)

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

// All four status-guarded transitions — RecordSent, Park, RecordFailure and
// RecordDeferral — report ErrTerminal when they touch zero rows, which means a
// newer attempt already closed this delivery. That is a benign no-op: turning
// it into a retry would put a finished delivery back on the ladder, turning it
// into an error would fail a job that did its work correctly, and reporting the
// disposition it was reaching for would claim a fact the row does not carry.
//
// One case per transition, because each is reached down a different path and a
// transition nothing routes to could never fail here.
func TestDispatchTreatsATerminalTransitionAsAlreadyHandled(t *testing.T) {
	for _, tc := range []struct {
		name string
		// store faults the ONE transition under test with ErrTerminal.
		store *fakeStore
		// route drives the dispatcher down the path that reaches it.
		route func(*fakeStore) *Dispatcher
	}{
		{
			"the send receipt", &fakeStore{delivery: liveDelivery(), sentErr: ErrTerminal},
			func(s *fakeStore) *Dispatcher { return newTestDispatcher(s, sendingResolver(), &stubConsent{}) },
		},
		{
			"a park", &fakeStore{delivery: liveDelivery(), parkErr: ErrTerminal},
			func(s *fakeStore) *Dispatcher {
				return newTestDispatcher(s, fakeResolver{err: ErrNoMailbox}, &stubConsent{})
			},
		},
		{
			"a failure note", &fakeStore{delivery: liveDelivery(), failedErr: ErrTerminal},
			func(s *fakeStore) *Dispatcher {
				return newTestDispatcher(s, fakeResolver{sender: &fakeSender{err: connector.ErrUnreachable}, granted: []string{sendScope}}, &stubConsent{})
			},
		},
		{
			"a pacing deferral", &fakeStore{delivery: liveDelivery(), deferErr: ErrTerminal},
			func(s *fakeStore) *Dispatcher {
				return newTestDispatcher(s, sendingResolver(), &stubConsent{}, waitPolicy{d: 90 * time.Second})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, wait, err := tc.route(tc.store).DispatchWithWait(context.Background(), tc.store.delivery.ID)
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if got != OutcomeSkipped || wait != 0 {
				t.Errorf("outcome=%v wait=%v, want OutcomeSkipped/0 — a newer attempt already closed this delivery", got, wait)
			}
		})
	}
}

// A CHANNEL send the provider accepted, whose receipt then could not be
// written, is a message the customer already has. Nothing can ask Telegram
// afterwards whether it went, and the next attempt would read the in-flight
// marker and park as an unknown outcome — recording a delivered message as
// never sent, in the log Art. 15 answers from.
//
// So it parks HERE, on this attempt, stating what is definitely true, and keeps
// the provider's message id: that id is the only handle anyone has on the
// message afterwards, and the failed receipt is the reason it is nowhere else.
// The gmail twin below (TestDispatchRetriesWhenATransitionFailsForANonTerminalReason)
// keeps retrying because a mail retry can discover its own prior send.
func TestAChannelSendWhoseReceiptFailsParksAsDelivered(t *testing.T) {
	channel := &stubMessageSender{}
	store := &fakeStore{delivery: channelDelivery(), sentErr: errors.New("connection reset by peer")}
	d := newTestDispatcher(store, fakeResolver{channel: channel}, &stubConsent{})

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if err != nil {
		t.Fatalf("dispatch: %v — a terminal outcome carrying an error fails the job that did its work", err)
	}
	if got != OutcomeParked {
		t.Fatalf("outcome = %v, want OutcomeParked — a message the provider accepted must not go back on a ladder that cannot tell it went", got)
	}
	if channel.calls != 1 {
		t.Fatalf("the provider was called %d time(s), want exactly 1", channel.calls)
	}
	if store.failed != "" {
		t.Errorf("a retry note %q was recorded; \"will retry\" is a false statement about a message that already went", store.failed)
	}
	if store.parked == unknownOutcomeReason {
		t.Errorf("the delivery parked as an unknown outcome; this outcome is KNOWN — the provider accepted the message")
	}
	if !strings.Contains(store.parked, "accepted") || !strings.Contains(store.parked, "receipt") {
		t.Errorf("park reason = %q; it must say the provider accepted the message and its receipt could not be recorded", store.parked)
	}
	if store.parkedReceipt != channelReceiptID {
		t.Errorf("provider message id kept on the park = %q, want %q — it is the only handle left on a message nothing else records",
			store.parkedReceipt, channelReceiptID)
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
			d := newTestDispatcher(tc.store, tc.resolver, &stubConsent{}, tc.policies...)

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
