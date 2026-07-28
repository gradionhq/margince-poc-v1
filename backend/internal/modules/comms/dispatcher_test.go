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

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The gates half of the dispatcher's spec: loading the delivery, resolving
// the mailbox, and the two fixed gates that can REFUSE a send. The policies
// that postpone one, and the transmission itself, are in
// dispatcher_transmit_test.go; the shared harness below serves both.
//
// The dispatcher's collaborators are all true boundaries — the database, the
// provider's transport, the consent authority, the clock — so they are the
// only things faked here. Nothing asserts call ORDER against a mock; the one
// ordering invariant that matters (authority refuses before consent answers)
// is proven by observing whether the consent authority was consulted at all.

type fakeStore struct {
	delivery Delivery
	loadErr  error

	sent     string
	parked   string
	failed   string
	deferred string

	// Per-transition faults. ErrTerminal from any of them is the benign
	// no-op the store documents: a newer attempt already closed the row.
	sentErr   error
	parkErr   error
	failedErr error
	deferErr  error
}

func (f *fakeStore) Load(context.Context, ids.UUID) (Delivery, error) { return f.delivery, f.loadErr }

func (f *fakeStore) RecordSent(_ context.Context, _ ids.UUID, p string) error {
	f.sent = p
	return f.sentErr
}

func (f *fakeStore) Park(_ context.Context, _ ids.UUID, r string) error {
	f.parked = r
	return f.parkErr
}

func (f *fakeStore) RecordFailure(_ context.Context, _ ids.UUID, r string) error {
	f.failed = r
	return f.failedErr
}

// RecordDeferral is a DISTINCT transition, not an alias of RecordFailure: it
// also gives back the attempt Load counted. Recording it separately here is
// what lets a test prove the dispatcher took the deferral path rather than
// noting a failure that would quietly spend a rung of the transmit ladder.
func (f *fakeStore) RecordDeferral(_ context.Context, _ ids.UUID, r string) error {
	f.deferred = r
	if f.deferErr == nil {
		f.delivery.Attempts = max(f.delivery.Attempts-1, 0)
	}
	return f.deferErr
}

type fakeSender struct {
	calls int
	seen  connector.OutboundMessage
	err   error
}

func (f *fakeSender) Send(_ context.Context, _ connector.Auth, m connector.OutboundMessage) (connector.SendReceipt, error) {
	f.calls++
	f.seen = m
	return connector.SendReceipt{ProviderMessageID: "gmsg1"}, f.err
}

type fakeResolver struct {
	sender  connector.Sender
	granted []string
	err     error
}

func (f fakeResolver) Resolve(context.Context, ids.UserID, string) (connector.Sender, connector.Auth, []string, error) {
	return f.sender, connector.Auth("cred"), f.granted, f.err
}

// stubConsent records WHO it was asked about, not only what it answered. The
// recipient list is the gate's whole subject: a gate handed the wrong
// addressees answers correctly about the wrong people, which is
// indistinguishable from a pass unless the argument itself is asserted.
type stubConsent struct {
	err   error
	asked []string
}

func (s *stubConsent) RequireGrantedForEmails(_ context.Context, recipients []string, _ string) error {
	s.asked = recipients
	return s.err
}

// stubSeats answers the live-seat gate. The zero value is a deactivated
// sender, so a test that means "still employed" has to say so.
type stubSeats struct {
	active bool
	reason string
	err    error
}

func (s stubSeats) ActiveSeat(context.Context, ids.UserID) (bool, string, error) {
	return s.active, s.reason, s.err
}

// liveSeat is the ordinary case every test that is not ABOUT the seat gate
// wants: the sender is still a permitted human.
func liveSeat() stubSeats { return stubSeats{active: true} }

const sendScope = "https://www.googleapis.com/auth/gmail.send"

var testNow = time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)

// testMaxAttempts stands in for the ladder length compose reads off River's
// job configuration; the exhaustion test pins a delivery against it.
const testMaxAttempts = 5

func liveDelivery() Delivery {
	return Delivery{
		ID: ids.NewV7(), UserID: ids.New[ids.UserKind](), Provider: "gmail",
		MessageID: "abc@margince.test", Recipients: []string{"buyer@example.com"},
		Cc: []string{"cc@example.com"}, ConsentPurpose: "marketing",
		Subject: "Re: pricing", Body: "As discussed.",
		InReplyTo: "anchor@example.com", References: []string{"anchor@example.com"},
		ListUnsubscribe: "<https://margince.test/u/tok>",
		Status:          StatusPending, Attempts: 1, CreatedAt: testNow.Add(-time.Minute),
	}
}

func newTestDispatcher(store deliveryStore, res ConnectionResolver, consent ConsentGate, policies ...SendPolicy) *Dispatcher {
	return newSeatedDispatcher(store, res, liveSeat(), consent, policies...)
}

// newSeatedDispatcher is newTestDispatcher with the seat authority spelled
// out, for the cases that are about the seat rather than about what comes
// after it.
func newSeatedDispatcher(store deliveryStore, res ConnectionResolver, seats SeatAuthority, consent ConsentGate, policies ...SendPolicy) *Dispatcher {
	return NewDispatcher(store, res, seats, consent, policies, func() time.Time { return testNow }, time.Hour, testMaxAttempts)
}

// dispatch runs one attempt and drops the postponement interval, which most
// cases here do not assert on. A case about the interval calls
// DispatchWithWait directly — the production caller always does, because a
// postponement the caller does not honor comes back on its own schedule
// rather than the one the policy asked for.
func dispatch(ctx context.Context, d *Dispatcher, id ids.UUID) (Outcome, error) {
	outcome, _, err := d.DispatchWithWait(ctx, id)
	return outcome, err
}

// A redelivered job must transmit nothing.
func TestDispatchOnATerminalDeliveryTransmitsNothing(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{loadErr: ErrTerminal}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

	got, err := dispatch(context.Background(), d, ids.NewV7())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSkipped || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d, want OutcomeSkipped/0", got, sender.calls)
	}
}

// A load that could not answer is an outage, not a verdict. Reading it as
// "already terminal" would silently drop a delivery that never went out.
func TestDispatchRetriesWhenTheDeliveryCannotBeLoaded(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{loadErr: errors.New("database timeout")}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

	got, err := dispatch(context.Background(), d, ids.NewV7())
	if got != OutcomeRetry || err == nil {
		t.Errorf("outcome=%v err=%v, want OutcomeRetry and the cause", got, err)
	}
	if sender.calls != 0 {
		t.Errorf("transmitted %d times without a loaded delivery", sender.calls)
	}
}

// A keyvault or database blip must not permanently kill a good send.
func TestDispatchRetriesOnATransientResolveFailure(t *testing.T) {
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{err: errors.New("keyvault timeout")}, &stubConsent{})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeRetry {
		t.Errorf("outcome = %v, want OutcomeRetry — a transient resolve fault is not fatal", got)
	}
	if store.parked != "" {
		t.Errorf("parked on a transient fault: %q", store.parked)
	}
}

func TestDispatchParksWhenTheUserHasNoMailbox(t *testing.T) {
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{err: ErrNoMailbox}, &stubConsent{})

	if got, _ := dispatch(context.Background(), d, store.delivery.ID); got != OutcomeParked {
		t.Errorf("outcome = %v, want OutcomeParked — there is nothing to retry against", got)
	}
}

// A connected mailbox whose connector cannot transmit is a fact about the
// deployment, not a fault: no retry turns a capture-only connector into a
// sender.
func TestDispatchParksWhenTheConnectorCannotSend(t *testing.T) {
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{err: ErrCannotSend}, &stubConsent{})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked {
		t.Errorf("outcome = %v, want OutcomeParked", got)
	}
	if store.parked == "" {
		t.Error("parked with no reason; an operator cannot act on that")
	}
}

func TestDispatchParksWhenTheGrantLacksSendScope(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{"readonly"}}, &stubConsent{})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d, want OutcomeParked/0", got, sender.calls)
	}
	if store.parked == "" {
		t.Error("parked with no reason; an operator cannot act on that")
	}
}

// A provider with no send capability at all has nothing to grant, so the
// authority gate must refuse it rather than fall through to a nil scope.
func TestDispatchParksWhenTheProviderCannotSendAtAll(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	store.delivery.Provider = "imap"
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, &stubConsent{})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d, want OutcomeParked/0", got, sender.calls)
	}
}

// THE load-bearing one: consent can be withdrawn between staging and transmit.
//
// The stub returns apperrors.ErrConsentNotGranted specifically, NOT a bare
// error — because only that sentinel is an ANSWER. A generic error means the
// check failed to run, which must retry, and the test below pins that apart.
func TestDispatchParksWhenConsentWasWithdrawnAfterStaging(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}},
		&stubConsent{err: apperrors.ErrConsentNotGranted})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d — a withdrawn consent must stop the send", got, sender.calls)
	}
}

// A consent service that is merely DOWN must not permanently destroy a
// consented send. Parking on any error would do exactly that.
func TestDispatchRetriesWhenTheConsentCheckFailsTransiently(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}},
		&stubConsent{err: errors.New("consent store timeout")})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeRetry {
		t.Errorf("outcome = %v, want OutcomeRetry — an outage is not a refusal", got)
	}
	if store.parked != "" {
		t.Errorf("parked on a transient consent fault: %q", store.parked)
	}
	if sender.calls != 0 {
		t.Errorf("transmitted %d times without a consent answer", sender.calls)
	}
}

// A send path with no consent authority wired is a deployment defect. Retrying
// would hide it behind a delivery that quietly never goes out.
func TestDispatchParksWhenNoConsentAuthorityIsWired(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, nil)

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d, want OutcomeParked/0", got, sender.calls)
	}
}

// Authority must refuse before consent answers, so the consent state is not
// observable to a caller with no rights.
func TestDispatchChecksAuthorityBeforeConsent(t *testing.T) {
	consulted := false
	consent := consentFunc(func(context.Context, []string, string) error { consulted = true; return nil })
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{}, granted: []string{"readonly"}}, consent)

	if _, err := dispatch(context.Background(), d, store.delivery.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if consulted {
		t.Error("consent was consulted despite an authority failure")
	}
}

type consentFunc func(context.Context, []string, string) error

func (f consentFunc) RequireGrantedForEmails(ctx context.Context, r []string, p string) error {
	return f(ctx, r, p)
}

// THE Cc one: a delivery stores its To and Cc apart because the wire needs
// them apart, and the authoritative gate is owed EVERY addressee. Asking about
// the To list alone leaves a cc'd person with no suppression at all — their
// one-click unsubscribe lands in the hours a paced batch sits staged and
// changes nothing about the message they receive.
func TestDispatchAsksConsentAboutEveryAddresseeIncludingCc(t *testing.T) {
	consent := &stubConsent{}
	store := &fakeStore{delivery: liveDelivery()}
	store.delivery.Recipients = []string{"buyer@example.com", "second@example.com"}
	store.delivery.Cc = []string{"cc@example.com", " Buyer@Example.com "}
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{}, granted: []string{sendScope}}, consent)

	if got, err := dispatch(context.Background(), d, store.delivery.ID); err != nil || got != OutcomeSent {
		t.Fatalf("outcome=%v err=%v, want OutcomeSent", got, err)
	}
	// The cc'd address is the assertion; the duplicate proves an addressee
	// listed twice is asked about once, the way a mail server reads it.
	want := []string{"buyer@example.com", "second@example.com", "cc@example.com"}
	if !slices.Equal(consent.asked, want) {
		t.Errorf("consent was asked about %v, want every addressee %v", consent.asked, want)
	}
}

// A cc'd recipient who withdrew after staging stops the whole message: one
// rendered message reaches every addressee, so it cannot go to some of them.
func TestDispatchParksWhenOnlyACcRecipientWithdrewConsent(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	// The gate refuses the list it is handed; handing it the To line alone
	// would never surface the cc'd withdrawal at all.
	consent := &stubConsent{err: apperrors.ErrConsentNotGranted}
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, consent)

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d — a withdrawn cc recipient must stop the send", got, sender.calls)
	}
	if !slices.Contains(consent.asked, "cc@example.com") {
		t.Errorf("consent was asked about %v — the cc'd addressee was never put to the gate", consent.asked)
	}
}

// Deactivation binds mid-flight. A staged batch survives its sender being
// off-boarded or compromised for as long as the maximum age allows, and the
// mailbox connection the provider still honours says nothing about whether
// this installation still permits the human it was lent by.
func TestDispatchParksWhenTheSenderIsNoLongerALiveSeat(t *testing.T) {
	sender := &fakeSender{}
	consent := &stubConsent{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newSeatedDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}},
		stubSeats{active: false, reason: "the sender's account is no longer active; a deactivated user's mailbox may not transmit staged messages"}, consent)

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d, want OutcomeParked/0 — a deactivated sender may not transmit", got, sender.calls)
	}
	if !strings.Contains(store.parked, "no longer active") {
		t.Errorf("parked reason = %q; an operator must be able to read WHY the batch stopped", store.parked)
	}
	// Authority-class, so it refuses before consent answers — the same
	// ordering the mailbox grant keeps.
	if consent.asked != nil {
		t.Errorf("consent was consulted about %v despite a dead seat", consent.asked)
	}
}

// A downgrade binds mid-flight the same way a deactivation does, and it must
// not be reported as one: a live read seat is not off-boarded, so the parked
// row has to carry the authority's OWN reason rather than the gate's hardcoded
// deactivation sentence — an operator reading the park record needs to tell
// the two apart.
func TestDispatchParksOnTheSeatAuthoritysOwnReasonRatherThanAHardcodedOne(t *testing.T) {
	sender := &fakeSender{}
	consent := &stubConsent{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newSeatedDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}},
		stubSeats{active: false, reason: "the sender holds a read-only seat; a read seat may not transmit staged messages"}, consent)

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d, want OutcomeParked/0 — a read-only seat may not transmit", got, sender.calls)
	}
	if !strings.Contains(store.parked, "read-only seat") || strings.Contains(store.parked, "no longer active") {
		t.Errorf("parked reason = %q; a live read seat must not be reported as a deactivated account", store.parked)
	}
}

// An identity store that is merely DOWN must not destroy every send in
// flight. Parking on a failure to get an answer would do exactly that.
func TestDispatchRetriesWhenTheSeatCheckFailsTransiently(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newSeatedDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}},
		stubSeats{err: errors.New("identity store timeout")}, &stubConsent{})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeRetry {
		t.Errorf("outcome = %v, want OutcomeRetry — an outage is not a deactivation", got)
	}
	if store.parked != "" {
		t.Errorf("parked on a transient seat fault: %q", store.parked)
	}
	if sender.calls != 0 {
		t.Errorf("transmitted %d times without a seat answer", sender.calls)
	}
}

// A send path with no seat authority wired is a deployment defect on the one
// lane that reaches a real external mailbox, so it fails closed.
func TestDispatchParksWhenNoSeatAuthorityIsWired(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newSeatedDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, nil, &stubConsent{})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d, want OutcomeParked/0", got, sender.calls)
	}
}

// A one-rung ladder is arithmetically positive and survives the default, but
// Load counts the attempt before the exhaustion guard reads it — so without a
// floor the guard would park every delivery before it ever asked a provider.
func TestADispatcherConfiguredWithOneRungStillTransmitsOnce(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := NewDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}},
		liveSeat(), &stubConsent{}, nil, func() time.Time { return testNow }, time.Hour, 1)

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSent || sender.calls != 1 {
		t.Errorf("outcome=%v calls=%d, want OutcomeSent/1 — the first attempt must reach the provider", got, sender.calls)
	}
}
