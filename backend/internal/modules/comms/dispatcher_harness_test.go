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
	// parkedReceipt is the provider message id a park kept, empty on an ordinary
	// park. It is the difference between "this delivery stopped" and "this
	// delivery reached the customer and here is the provider's own id for it".
	parkedReceipt string
	// stamped is the RFC822 identity the receipt carried through to the store.
	// The dispatcher must hand the WHOLE receipt on: dropping this field on
	// the floor here is invisible until a sent message is filed under an
	// identity no reply will ever quote.
	stamped  string
	deferred string

	// marked and cleared count the at-most-once marker's two transitions. They
	// are COUNTS rather than flags because the invariant is about how many times
	// each happened relative to the provider call: one mark before a
	// transmission, and a retraction only on a definite answer.
	marked  int
	cleared int

	// Per-transition faults. ErrTerminal from any of them is the benign
	// no-op the store documents: a newer attempt already closed the row.
	sentErr   error
	parkErr   error
	failedErr error
	deferErr  error
	markErr   error
	clearErr  error
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

// ParkTransmitted shares parkErr with Park — both are the same guarded
// transition to 'parked' — but records the receipt it kept SEPARATELY, because
// that is the fact this park exists for: a message the provider accepted, whose
// id would otherwise be the only thing lost.
func (f *fakeStore) ParkTransmitted(_ context.Context, _ ids.UUID, reason, providerMessageID string) error {
	f.parked = reason
	f.parkedReceipt = providerMessageID
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

// MarkInFlight records the pre-call marker the fake delivery ALSO carries
// forward, so a second dispatch of the same fake store sees what the first one
// wrote. Without that the crash case could not be exercised at all: the whole
// guarantee is that the marker outlives the attempt that set it.
func (f *fakeStore) MarkInFlight(_ context.Context, _ ids.UUID) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.marked++
	at := testNow
	f.delivery.InFlightAt = &at
	return nil
}

func (f *fakeStore) ClearInFlight(_ context.Context, _ ids.UUID) error {
	if f.clearErr != nil {
		return f.clearErr
	}
	f.cleared++
	f.delivery.InFlightAt = nil
	return nil
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

// carryingSender is fakeSender that declares it can transmit files, which is
// what lets a case reach the integrity gate: the carriage gate refuses first on
// a channel that cannot carry them at all, and would mask every case about the
// files themselves.
type carryingSender struct {
	fakeSender
}

func (c *carryingSender) CarriesAttachments() bool { return true }

var _ connector.AttachmentCarrier = (*carryingSender)(nil)

// stubMessageSender is the CHANNEL provider boundary for the cases that run
// against the fake store. It is spelled apart from fakeSender because the two
// seams differ in the one way these cases are about: nothing at this provider
// can be asked afterwards whether a message already went.
type stubMessageSender struct {
	calls int
	err   error
}

func (s *stubMessageSender) SendMessage(context.Context, connector.Auth, connector.ChannelMessage) (connector.SendReceipt, error) {
	s.calls++
	return connector.SendReceipt{ProviderMessageID: channelReceiptID}, s.err
}

// channelReceiptID is what the stub provider hands back — Telegram's own
// message id, the value a park after a failed receipt has to keep.
const channelReceiptID = "9911"

type fakeResolver struct {
	sender  connector.EmailSender
	channel connector.MessageSender
	granted []string
	err     error
}

func (f fakeResolver) Resolve(context.Context, ids.UserID, string) (connector.EmailSender, connector.Auth, []string, error) {
	return f.sender, connector.Auth("cred"), f.granted, f.err
}

// ResolveChannel answers with the bot token itself as the credential, which is
// what the real resolver hands back: a channel binding has no OAuth bundle and
// therefore no scope list either.
func (f fakeResolver) ResolveChannel(context.Context, string) (connector.MessageSender, connector.Auth, error) {
	return f.channel, connector.Auth("bot-token"), f.err
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

// channelDelivery is liveDelivery's channel-shaped twin: the same staged
// message on the transport whose retries cannot detect a prior send. The mail
// fields are absent rather than zeroed by accident — a channel message has no
// subject, no address list and no RFC822 identity — and ChannelUserID being
// non-nil is what the dispatcher reads as the row's shape.
func channelDelivery() Delivery {
	recipient := "778899"
	return Delivery{
		ID: ids.NewV7(), UserID: ids.New[ids.UserKind](), Provider: "telegram",
		ChannelUserID: &recipient, ConsentPurpose: "transactional",
		Body: "On its way today.", InReplyTo: "4231",
		Status: StatusPending, Attempts: 1, CreatedAt: testNow.Add(-time.Minute),
	}
}

func newTestDispatcher(store deliveryStore, res ConnectionResolver, consent ConsentGate, policies ...SendPolicy) *Dispatcher {
	return newSeatedDispatcher(store, res, liveSeat(), consent, policies...)
}

// newSeatedDispatcher is newTestDispatcher with the seat authority spelled
// out, for the cases that are about the seat rather than about what comes
// after it.
func newSeatedDispatcher(store deliveryStore, res ConnectionResolver, seats SeatAuthority, consent ConsentGate, policies ...SendPolicy) *Dispatcher {
	// A nil attachment authority is the fail-closed default, and it is inert
	// for every delivery here: the integrity gate only asks when a delivery
	// actually carries files, and these carry none. A case about attachments
	// uses newAttachmentDispatcher and passes its own stub.
	return NewDispatcher(store, res, seats, nil, consent, policies, func() time.Time { return testNow }, time.Hour, testMaxAttempts)
}

// newAttachmentDispatcher is newSeatedDispatcher with the attachment authority
// under test wired, and a live seat, so a case can assert on the integrity gate
// without restating the rest of the chain.
func newAttachmentDispatcher(store deliveryStore, res ConnectionResolver, files AttachmentAuthority) *Dispatcher {
	return NewDispatcher(store, res, stubSeats{active: true}, files, &stubConsent{}, nil,
		func() time.Time { return testNow }, time.Hour, testMaxAttempts)
}

// stubAttachments is a scripted AttachmentAuthority: it records what it was
// asked about, so a case can prove the gate asked at all.
type stubAttachments struct {
	ok     bool
	reason string
	err    error
	asked  []ids.UUID
}

func (s *stubAttachments) EnsureTransmittable(_ context.Context, _ ids.UserID, attachmentIDs []ids.UUID) (bool, string, error) {
	s.asked = append(s.asked, attachmentIDs...)
	return s.ok, s.reason, s.err
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
