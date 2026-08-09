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
// the mailbox, and the fixed gates that can REFUSE a send. The policies that
// postpone one and the transmission itself are in
// dispatcher_transmit_test.go, what an attempt records is in
// dispatcher_reason_test.go, and the fakes all three ride are in
// dispatcher_harness_test.go.

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

// A provider this installation has no integration for is a fact, not an
// outage. Read as transient it fails identically on every rung, and the
// exhaustion guard sits AFTER the resolve — so the row would outlive its job
// and stay pending forever, looking live and never sending.
func TestDispatchParksWhenTheProviderHasNoConfiguredIntegration(t *testing.T) {
	sender := &fakeSender{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{err: ErrProviderNotConfigured}, &stubConsent{})

	got, _ := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeParked || sender.calls != 0 {
		t.Errorf("outcome=%v calls=%d, want OutcomeParked/0", got, sender.calls)
	}
	if !strings.Contains(store.parked, "gmail") {
		t.Errorf("parked reason = %q; it must name the integration an operator has to configure", store.parked)
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
	consent := consentFunc(func(context.Context, []connector.Recipient, string) error { consulted = true; return nil })
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{}, granted: []string{"readonly"}}, consent)

	if _, err := dispatch(context.Background(), d, store.delivery.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if consulted {
		t.Error("consent was consulted despite an authority failure")
	}
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

// A recipient stored with surrounding whitespace is ONE addressee, and the gate
// must be handed the same spelling this helper deduped on. Handing on the padded
// original refuses a legitimate send: the gate matches an address, finds
// nothing, and the delivery parks as "consent not granted" — a valid message
// killed under a reason that reads as a recipient who opted out.
func TestDispatchAsksConsentAboutTheNormalizedAddressItDedupedOn(t *testing.T) {
	consent := &stubConsent{}
	store := &fakeStore{delivery: liveDelivery()}
	store.delivery.Recipients = []string{"  Buyer@Example.com\t"}
	store.delivery.Cc = nil
	d := newTestDispatcher(store, sendingResolver(), consent)

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if err != nil || got != OutcomeSent {
		t.Fatalf("outcome=%v err=%v, want OutcomeSent", got, err)
	}
	if !slices.Equal(consent.asked, []string{"buyer@example.com"}) {
		t.Errorf("consent was asked about %v, want the normalized address the dedupe keyed on", consent.asked)
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
		liveSeat(), nil, &stubConsent{}, nil, func() time.Time { return testNow }, time.Hour, 1)

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomeSent || sender.calls != 1 {
		t.Errorf("outcome=%v calls=%d, want OutcomeSent/1 — the first attempt must reach the provider", got, sender.calls)
	}
}

// A channel that cannot carry files PARKS the message rather than transmitting
// the text alone. Stripping is the one behaviour ADR-0086 exists to forbid,
// because it fails silently: the send succeeds, the timeline shows an
// attachment chip because it records what was STAGED, and only the recipient —
// who sees a message referring to a file that is not there — knows.
func TestACarriageMismatchParksAndNamesTheFiles(t *testing.T) {
	store := &fakeStore{}
	d := NewDispatcher(store, fakeResolver{}, liveSeat(), nil, &stubConsent{}, nil,
		func() time.Time { return testNow }, time.Hour, 3)
	del := Delivery{
		ID: ids.NewV7(), Provider: "telegram",
		Attachments: []OutboundFile{{AttachmentID: ids.NewV7(), Filename: "contract.pdf"}},
	}

	outcome, _, err := d.gateAttachmentCarriage(context.Background(), del,
		sendSeam{carriesAttachments: false})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if outcome != OutcomeParked {
		t.Fatalf("outcome = %q, want parked — the alternative is a message that lies about its contents", outcome)
	}
	// The reason names the channel AND the file: "this could not be sent" with
	// no subject leaves a person guessing which of the two to fix.
	for _, want := range []string{"telegram", "contract.pdf"} {
		if !strings.Contains(store.parked, want) {
			t.Errorf("park reason %q does not name %q", store.parked, want)
		}
	}
}

func TestACarryingChannelIsNotParkedForItsFiles(t *testing.T) {
	store := &fakeStore{}
	d := NewDispatcher(store, fakeResolver{}, liveSeat(), nil, &stubConsent{}, nil,
		func() time.Time { return testNow }, time.Hour, 3)
	del := Delivery{
		ID: ids.NewV7(), Provider: "gmail",
		Attachments: []OutboundFile{{AttachmentID: ids.NewV7(), Filename: "contract.pdf"}},
	}
	outcome, _, err := d.gateAttachmentCarriage(context.Background(), del,
		sendSeam{carriesAttachments: true})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if outcome != outcomeUndecided {
		t.Errorf("outcome = %q, want undecided — a channel that declared carriage sends its files", outcome)
	}
}

// The overwhelming majority of deliveries carry no files. A gate that parked
// them would stop every send on every channel that never declared carriage.
func TestAMessageWithNoFilesIsNeverParkedForCarriage(t *testing.T) {
	store := &fakeStore{}
	d := NewDispatcher(store, fakeResolver{}, liveSeat(), nil, &stubConsent{}, nil,
		func() time.Time { return testNow }, time.Hour, 3)
	outcome, _, err := d.gateAttachmentCarriage(context.Background(),
		Delivery{ID: ids.NewV7(), Provider: "telegram"}, sendSeam{carriesAttachments: false})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if outcome != outcomeUndecided {
		t.Errorf("outcome = %q, want undecided for a message carrying no files", outcome)
	}
}
