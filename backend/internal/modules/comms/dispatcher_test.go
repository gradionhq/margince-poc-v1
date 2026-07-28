// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"context"
	"errors"
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

	sent   string
	parked string
	failed string

	// Per-transition faults. ErrTerminal from any of them is the benign
	// no-op the store documents: a newer attempt already closed the row.
	sentErr   error
	parkErr   error
	failedErr error
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

type stubConsent struct{ err error }

func (s stubConsent) RequireGrantedForEmails(context.Context, []string, string) error { return s.err }

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
	return NewDispatcher(store, res, consent, policies, func() time.Time { return testNow }, time.Hour, testMaxAttempts)
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
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

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
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

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
	d := newTestDispatcher(store, fakeResolver{err: errors.New("keyvault timeout")}, stubConsent{})

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
	d := newTestDispatcher(store, fakeResolver{err: ErrNoMailbox}, stubConsent{})

	if got, _ := dispatch(context.Background(), d, store.delivery.ID); got != OutcomeParked {
		t.Errorf("outcome = %v, want OutcomeParked — there is nothing to retry against", got)
	}
}

// A connected mailbox whose connector cannot transmit is a fact about the
// deployment, not a fault: no retry turns a capture-only connector into a
// sender.
func TestDispatchParksWhenTheConnectorCannotSend(t *testing.T) {
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{err: ErrCannotSend}, stubConsent{})

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
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{"readonly"}}, stubConsent{})

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
	d := newTestDispatcher(store, fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{})

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
		stubConsent{err: apperrors.ErrConsentNotGranted})

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
		stubConsent{err: errors.New("consent store timeout")})

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
