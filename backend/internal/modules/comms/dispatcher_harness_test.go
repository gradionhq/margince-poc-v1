// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"context"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The shared harness every dispatcher suite in this package rides —
// dispatcher_test.go (the gates that refuse), dispatcher_transmit_test.go (the
// policies that postpone and the message on the wire), and
// dispatcher_reason_test.go (what an attempt records).
//
// The dispatcher's collaborators are all true boundaries — the database, the
// provider's transport, the consent authority, the seat authority, the clock —
// so they are the only things faked here. Nothing asserts call ORDER against a
// mock; the one ordering invariant that matters (authority refuses before
// consent answers) is proven by observing whether the consent authority was
// consulted at all.

type fakeStore struct {
	delivery Delivery
	loadErr  error

	sent   string
	parked string
	failed string
	// stamped is the RFC822 identity the receipt carried through to the store.
	// The dispatcher must hand the WHOLE receipt on: dropping this field on
	// the floor here is invisible until a sent message is filed under an
	// identity no reply will ever quote.
	stamped  string
	deferred string

	// Per-transition faults. ErrTerminal from any of them is the benign
	// no-op the store documents: a newer attempt already closed the row.
	sentErr   error
	parkErr   error
	failedErr error
	deferErr  error
}

func (f *fakeStore) Load(context.Context, ids.UUID) (Delivery, error) { return f.delivery, f.loadErr }

func (f *fakeStore) RecordSent(_ context.Context, _ ids.UUID, receipt connector.SendReceipt) error {
	f.sent = receipt.ProviderMessageID
	f.stamped = receipt.RFC822MessageID
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
	seen  connector.EmailMessage
	err   error
}

func (f *fakeSender) SendEmail(_ context.Context, _ connector.Auth, m connector.EmailMessage) (connector.SendReceipt, error) {
	f.calls++
	f.seen = m
	return connector.SendReceipt{ProviderMessageID: "gmsg1", RFC822MessageID: "stamped@mail.gmail.com"}, f.err
}

type fakeResolver struct {
	sender  connector.EmailSender
	granted []string
	err     error
}

func (f fakeResolver) Resolve(context.Context, ids.UserID, string) (connector.EmailSender, connector.Auth, []string, error) {
	return f.sender, connector.Auth("cred"), f.granted, f.err
}

// stubConsent records WHO it was asked about, not only what it answered. The
// recipient list is the gate's whole subject: a gate handed the wrong
// addressees answers correctly about the wrong people, which is
// indistinguishable from a pass unless the argument itself is asserted.
//
// It records each recipient's own LABEL — the address for mail, provider:account
// for a channel — because that is the fact every case here asserts, and because
// a stub that flattened a channel recipient to its empty Email field would let a
// delivery reach the gate naming nobody and still read as asked-about.
type stubConsent struct {
	err   error
	asked []string
}

func (s *stubConsent) RequireGrantedForRecipients(_ context.Context, recipients []connector.Recipient, _ string) error {
	s.asked = nil
	for _, r := range recipients {
		if r.Channel != nil {
			s.asked = append(s.asked, r.Channel.Provider+":"+r.Channel.ChannelUserID)
			continue
		}
		s.asked = append(s.asked, r.Email)
	}
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

type consentFunc func(context.Context, []connector.Recipient, string) error

func (f consentFunc) RequireGrantedForRecipients(ctx context.Context, r []connector.Recipient, p string) error {
	return f(ctx, r, p)
}
