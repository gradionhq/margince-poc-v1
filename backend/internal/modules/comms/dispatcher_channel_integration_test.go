// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// Dispatching a CHANNEL delivery, over a migrated Postgres (telegram-oa design
// §8.3, §8.4).
//
// The provider boundary is faked — no test reaches a real Telegram — but the
// delivery ROW is not, because that is where park-versus-retry actually lives.
// The at-most-once guarantee is a claim about what a durable column says before
// the provider call and what the next attempt does with it, and a fake store
// could be made to agree with any implementation.
//
// It rides the shared fixture in store_integration_test.go
// (storeEnv/setupStore/stageChannel/deliveryRow) and the fakes in
// dispatcher_harness_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// channelRecipient is the Telegram account id every case here replies to, and
// channelAnchor the inbound message they anchor on.
const (
	channelRecipient = "778899"
	channelAnchor    = "4231"
)

// fakeChannelSender is the provider boundary of the channel seam — the one thing
// these cases fake, because no test reaches a real Telegram. It records every
// message it was handed rather than only the last: the property under test is HOW
// MANY times a customer was messaged.
type fakeChannelSender struct {
	sent []connector.ChannelMessage
	err  error
}

func (f *fakeChannelSender) SendMessage(_ context.Context, _ connector.Auth, m connector.ChannelMessage) (connector.SendReceipt, error) {
	f.sent = append(f.sent, m)
	if f.err != nil {
		return connector.SendReceipt{}, f.err
	}
	return connector.SendReceipt{ProviderMessageID: "9911"}, nil
}

// inFlightAt reads the at-most-once marker straight from the row. Read through
// the pool rather than through Load, which counts an attempt of its own and
// would change the state under test.
func (e *storeEnv) inFlightAt(t *testing.T, id ids.UUID) *time.Time {
	t.Helper()
	var at *time.Time
	if err := database.WithWorkspaceTx(e.ctx, e.store.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `SELECT inflight_at FROM comms_outbound WHERE id = $1`, id).Scan(&at)
	}); err != nil {
		t.Fatalf("reading the in-flight marker of %s: %v", id, err)
	}
	return at
}

// stageReply stages one channel delivery: a Telegram reply on a conversation the
// customer opened, which is the only channel send V1 permits.
func (e *storeEnv) stageReply(t *testing.T) ids.UUID {
	t.Helper()
	return e.stageChannel(t, StageChannelInput{
		ActivityID: e.telegramActivity(t),
		Provider:   "telegram",
		Recipient: connector.ChannelIdentity{
			Provider: "telegram", ChannelUserID: channelRecipient, Username: "buyer",
		},
		Body:           "On its way today.",
		ConsentPurpose: "transactional",
		ReplyTo:        channelAnchor,
	})
}

// channelLadder is the retry ladder every case here runs against: long enough
// that no case is decided by exhaustion, since that bound has its own suite
// (dispatcher_ladder_integration_test.go). What is under test here is which
// disposition one attempt reaches, not when the rungs run out.
const channelLadder = 5

// channelDispatcher assembles the production dispatcher over the real store, with
// the provider boundary — and only that — faked. The mail seam is handed in too,
// so a case can prove it was never called.
func (e *storeEnv) channelDispatcher(channel *fakeChannelSender, mail *fakeSender, consent ConsentGate) *Dispatcher {
	return NewDispatcher(e.store,
		fakeResolver{sender: mail, channel: channel, granted: []string{sendScope}},
		liveSeat(), nil, consent, nil,
		func() time.Time { return e.clockValue }, time.Hour, channelLadder)
}

// The routing itself: a channel-shaped row reaches the MESSAGE seam, with every
// staged field mapped onto it — and the MAIL seam is never touched. A delivery
// misrouted the other way would hand a channel body to a mail connector, whose
// identity validation rejects anything without an '@', so the reply would die at
// the seam with a reason that names an RFC822 rule the rep never invoked.
func TestDispatcherRoutesAChannelDeliveryToMessageSender(t *testing.T) {
	e := setupStore(t)
	id := e.stageReply(t)

	channel := &fakeChannelSender{}
	mail := &fakeSender{}
	consent := &stubConsent{}
	d := e.channelDispatcher(channel, mail, consent)

	outcome, wait, err := d.DispatchWithWait(e.ctx, id)
	if err != nil || outcome != OutcomeSent {
		t.Fatalf("dispatch → %v/%v (%v), want OutcomeSent", outcome, wait, err)
	}
	if mail.calls != 0 {
		t.Fatalf("the mail seam was called %d time(s) for a channel delivery", mail.calls)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("the message seam saw %d message(s), want exactly 1", len(channel.sent))
	}
	got := channel.sent[0]
	if got.Recipient.Provider != "telegram" || got.Recipient.ChannelUserID != channelRecipient {
		t.Errorf("recipient = %+v, want the row's provider and account id", got.Recipient)
	}
	// The username is display-only and must NOT travel: a handle can be released
	// and re-claimed, so routing on it would deliver to whoever holds it today.
	if got.Recipient.Username != "" {
		t.Errorf("recipient carried username %q; nothing may route on a re-claimable handle", got.Recipient.Username)
	}
	if got.Body != "On its way today." {
		t.Errorf("body = %q, want the staged text", got.Body)
	}
	if got.ReplyTo != channelAnchor {
		t.Errorf("reply anchor = %q, want %q — an unanchored reply reads to the customer as a message out of nowhere", got.ReplyTo, channelAnchor)
	}
	if got.IdempotencyKey != id.String() {
		t.Errorf("idempotency key = %q, want the delivery id %q", got.IdempotencyKey, id)
	}
	// Load counted this attempt before the dispatcher reached the seam, so a
	// FIRST transmission must still report zero prior ones.
	if got.Attempt != 0 {
		t.Errorf("first transmission reported Attempt = %d, want 0", got.Attempt)
	}
	// The consent gate is asked about the CHANNEL recipient, not an empty mail
	// address list: a default-deny gate asked about nobody refuses nobody.
	if len(consent.asked) != 1 || consent.asked[0] != "telegram:"+channelRecipient {
		t.Errorf("consent was asked about %v, want the one channel recipient", consent.asked)
	}

	status, attempts, _ := e.deliveryRow(t, id)
	if status != StatusSent || attempts != 1 {
		t.Fatalf("row = %q/%d attempts, want sent/1", status, attempts)
	}
	// The marker is retracted by the receipt: the outcome is known now, and a
	// sent row still claiming a transmission in flight would read as a message
	// nobody can account for.
	if at := e.inFlightAt(t, id); at != nil {
		t.Errorf("in-flight marker still set at %v on a delivery whose receipt is recorded", at)
	}
}

// The safety property of the whole channel path: an outcome the provider never
// reported is NEVER retried.
//
// Both halves are proven, because only together do they say "never". The first
// is the outcome the dispatcher sees itself — it parks instead of asking again.
// The second is the crash: an attempt that marked itself in flight and then died
// before recording anything leaves a pending row that looks untried, and the
// redelivered job must refuse to transmit rather than deliver a second copy to a
// customer with nothing able to detect it.
func TestChannelDeliveryParksOnAnUnknownOutcome(t *testing.T) {
	t.Run("the attempt that gets no answer parks rather than retrying", func(t *testing.T) {
		e := setupStore(t)
		id := e.stageReply(t)

		unanswered := errors.New("telegram: sendMessage: context deadline exceeded")
		channel := &fakeChannelSender{err: errors.Join(connector.ErrSendOutcomeUnknown, unanswered)}
		d := e.channelDispatcher(channel, &fakeSender{}, &stubConsent{})

		outcome, _, err := d.DispatchWithWait(e.ctx, id)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		// OutcomeRetry here would be the defect: the runner would back off and
		// come back, and the second call would be a second real message.
		if outcome != OutcomeParked {
			t.Fatalf("outcome = %v, want OutcomeParked — an unanswered transmission must never go back on the ladder", outcome)
		}
		if len(channel.sent) != 1 {
			t.Fatalf("the provider saw %d message(s) on one dispatch, want 1", len(channel.sent))
		}
		status, _, reason := e.deliveryRow(t, id)
		if status != StatusParked {
			t.Fatalf("status = %q, want parked", status)
		}
		// The row has to say WHAT is uncertain and WHO decides, because nothing
		// automatic can: the message may already be with the customer.
		if !strings.Contains(reason, "never confirmed") || !strings.Contains(reason, "will not be retried") {
			t.Errorf("park reason = %q; it must say the outcome is unknown and that nothing will retry it", reason)
		}
		// The marker STAYS: it is the durable record that a message may have gone
		// out, and clearing it would erase the only evidence of the uncertainty.
		if e.inFlightAt(t, id) == nil {
			t.Error("the in-flight marker was cleared on an unknown outcome; the row no longer records that a message may have been sent")
		}

		// And the delivery is terminal, so a redelivered job stops at Load.
		if _, err := e.store.Load(e.ctx, id); !errors.Is(err, ErrTerminal) {
			t.Fatalf("re-loading the parked delivery: %v, want ErrTerminal", err)
		}
	})

	t.Run("a crashed attempt's marker stops the next one from transmitting", func(t *testing.T) {
		e := setupStore(t)
		id := e.stageReply(t)

		// Exactly what a killed worker leaves behind: the marker committed
		// before the provider call, and no outcome recorded after it. The row is
		// still pending, so the runner will redeliver.
		if err := e.store.MarkInFlight(e.ctx, id); err != nil {
			t.Fatalf("marking the transmission in flight: %v", err)
		}

		// The sender would SUCCEED if it were called, which is the point: only a
		// refusal to call it can be the reason nothing is sent.
		channel := &fakeChannelSender{}
		d := e.channelDispatcher(channel, &fakeSender{}, &stubConsent{})

		outcome, _, err := d.DispatchWithWait(e.ctx, id)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if outcome != OutcomeParked {
			t.Fatalf("outcome = %v, want OutcomeParked", outcome)
		}
		if len(channel.sent) != 0 {
			t.Fatalf("the provider was handed %d message(s) after a prior attempt was already in flight; the customer has now been messaged twice", len(channel.sent))
		}
		status, _, reason := e.deliveryRow(t, id)
		if status != StatusParked || !strings.Contains(reason, "never confirmed") {
			t.Fatalf("row = %q/%q, want parked with the unknown-outcome reason", status, reason)
		}
	})
}

// The other direction, which the never-retry rule must not swallow: a DEFINITE
// refusal from Telegram proves the message did not go, so it retracts the marker
// and the delivery goes back on the ladder. Without the retraction the first
// transient refusal would park every message permanently — the guard would have
// turned an ordinary failure into a dead delivery.
func TestChannelDeliveryRetriesOnADefiniteError(t *testing.T) {
	e := setupStore(t)
	id := e.stageReply(t)

	// A refusal on Telegram's own terms: it understood the request and answered.
	// The send did not happen, so trying again is safe.
	refused := errors.New("telegram: sendMessage: Bad Request: chat not found")
	channel := &fakeChannelSender{err: refused}
	d := e.channelDispatcher(channel, &fakeSender{}, &stubConsent{})

	outcome, _, err := d.DispatchWithWait(e.ctx, id)
	if outcome != OutcomeRetry {
		t.Fatalf("outcome = %v, want OutcomeRetry — a definite refusal transmitted nothing", outcome)
	}
	if err == nil || !errors.Is(err, refused) {
		t.Fatalf("returned error = %v; the full cause must reach the job log", err)
	}
	status, attempts, reason := e.deliveryRow(t, id)
	if status != StatusPending {
		t.Fatalf("status = %q, want pending — a retryable fault is not a verdict", status)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1: the attempt reached the provider and spends a rung", attempts)
	}
	if !strings.HasPrefix(reason, faultPrefix) {
		t.Errorf("reason = %q, want it labelled as a transient fault", reason)
	}
	if at := e.inFlightAt(t, id); at != nil {
		t.Fatalf("in-flight marker still set at %v after a definite refusal; the next attempt would park a message that never went", at)
	}

	// The proof that the retraction is real rather than cosmetic: the next
	// attempt actually transmits.
	channel.err = nil
	if outcome, _, err := d.DispatchWithWait(e.ctx, id); err != nil || outcome != OutcomeSent {
		t.Fatalf("second dispatch → %v (%v), want OutcomeSent", outcome, err)
	}
	if len(channel.sent) != 2 {
		t.Fatalf("the provider saw %d attempt(s) across two dispatches, want 2", len(channel.sent))
	}
	if channel.sent[1].Attempt != 1 {
		t.Errorf("the retry reported Attempt = %d, want 1 — a retry must be distinguishable from a first send", channel.sent[1].Attempt)
	}
	if status, _, _ := e.deliveryRow(t, id); status != StatusSent {
		t.Fatalf("status = %q after the successful retry, want sent", status)
	}
}

// A 429 is a definite answer too, and Telegram states WHEN to come back. The
// postponement must be that interval and not the ladder's own backoff: Telegram
// paces roughly one message per second per chat, so guessing shorter earns a
// harder limit on the bot the whole workspace shares.
func TestChannelDeliveryHonoursRetryAfterOn429(t *testing.T) {
	e := setupStore(t)
	id := e.stageReply(t)

	const stated = 42 * time.Second
	channel := &fakeChannelSender{err: &connector.RateLimitedError{RetryAfter: stated}}
	d := e.channelDispatcher(channel, &fakeSender{}, &stubConsent{})

	outcome, wait, err := d.DispatchWithWait(e.ctx, id)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if outcome != OutcomePostponed {
		t.Fatalf("outcome = %v, want OutcomePostponed", outcome)
	}
	if wait != stated {
		t.Fatalf("wait = %v, want %v — the provider named the interval and it is not ours to shorten", wait, stated)
	}
	status, attempts, reason := e.deliveryRow(t, id)
	if status != StatusPending {
		t.Fatalf("status = %q, want pending: a throttled message may still go", status)
	}
	// A throttle REACHED the provider, so it keeps its rung — otherwise a
	// sustained limit snoozes forever with nothing bounding it.
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1: a 429 is an answer from the provider, so the attempt counts", attempts)
	}
	if !strings.Contains(reason, "rate limiting") {
		t.Errorf("reason = %q; an operator must be able to see which side held the message back", reason)
	}
	// The message did not go, so the marker must not be left claiming it might
	// have: the retry this postponement asks for has to be allowed to transmit.
	if at := e.inFlightAt(t, id); at != nil {
		t.Fatalf("in-flight marker still set at %v after a 429; the postponed retry would park instead of sending", at)
	}
}

// carryingChannelSender is fakeChannelSender that DECLARES carriage, which is
// what lets a case reach the transmit at all: the carriage gate parks a message
// with files against a sender that declared none, and no production channel
// connector declares any yet.
type carryingChannelSender struct {
	fakeChannelSender
}

func (c *carryingChannelSender) Carriage() connector.Carriage {
	return connector.Carriage{Carries: true, MaxFiles: 10, MaxBytesPerFile: 25 << 20, MaxBodyWithFiles: 1024}
}

var _ connector.AttachmentCarrier = (*carryingChannelSender)(nil)

// A file staged on a channel reply reaches the provider WHOLE — its snapshot
// identity and its bytes — over the real store and the real dispatcher.
//
// This is the one hop the rest of the arc cannot prove: everything above it can
// be green while ChannelMessage.Files arrives empty, and the failure is
// invisible until a customer gets a message referring to a file that is not
// there. The set travels under the same all-or-nothing obligation the mail seam
// carries, which is why the count is asserted before anything about the content.
func TestAChannelReplyHandsItsFilesToTheProvider(t *testing.T) {
	e := setupStore(t)
	attachment := ids.NewV7()
	id := e.stageChannel(t, StageChannelInput{
		ActivityID: e.telegramActivity(t),
		Provider:   "telegram",
		Recipient: connector.ChannelIdentity{
			Provider: "telegram", ChannelUserID: channelRecipient,
		},
		Body:           "the quote you asked for",
		ConsentPurpose: "transactional",
		Attachments: []OutboundFile{{
			AttachmentID: attachment, Filename: "quote.pdf",
			ContentType: "application/pdf", ByteSize: 4096, Checksum: "sha256:x",
		}},
	})

	channel := &carryingChannelSender{}
	files := &stubAttachments{ok: true}
	d := NewDispatcher(e.store,
		fakeResolver{sender: &fakeSender{}, channel: channel, granted: []string{sendScope}},
		liveSeat(), files, &stubConsent{}, nil,
		func() time.Time { return e.clockValue }, time.Hour, channelLadder)

	outcome, _, err := d.DispatchWithWait(e.ctx, id)
	if err != nil || outcome != OutcomeSent {
		t.Fatalf("dispatch → %v (%v), want OutcomeSent", outcome, err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("the message seam saw %d message(s), want exactly 1", len(channel.sent))
	}
	got := channel.sent[0].Files
	if len(got) != 1 {
		t.Fatalf("the provider was handed %d file(s) for a reply staged with 1 — an adapter may never "+
			"transmit a set that differs from the one it was handed", len(got))
	}
	if got[0].AttachmentID != attachment.String() || got[0].Filename != "quote.pdf" {
		t.Errorf("the file reached the provider as %+v, want the staged snapshot", got[0])
	}
	// The bytes travel too: a part with no content is what a recipient sees as
	// an empty attachment.
	if len(got[0].Body) == 0 {
		t.Error("the file reached the provider with no bytes")
	}
	// And the read went through the authority, not around it.
	if len(files.read) != 1 || files.read[0] != attachment {
		t.Errorf("the transmit read %v from the attachment authority, want the one staged id", files.read)
	}
}

// A file the object store cannot produce leaves the message RETRYABLE, never
// parked as one whose outcome nobody learned.
//
// The channel seam cannot detect a prior send, so it commits an in-flight marker
// and treats any later attempt as "this may already have gone". A byte read that
// happened after that marker would turn every transient store fault — and every
// deterministic over-size refusal — into a park telling the rep their message
// may have arrived and discouraging a resend. Nothing was transmitted here, and
// the disposition has to say so.
func TestAChannelReplyWhoseFilesCannotBeReadStaysRetryable(t *testing.T) {
	e := setupStore(t)
	id := e.stageChannel(t, StageChannelInput{
		ActivityID: e.telegramActivity(t),
		Provider:   "telegram",
		Recipient: connector.ChannelIdentity{
			Provider: "telegram", ChannelUserID: channelRecipient,
		},
		Body:           "the quote you asked for",
		ConsentPurpose: "transactional",
		Attachments:    []OutboundFile{{AttachmentID: ids.NewV7(), Filename: "quote.pdf"}},
	})

	channel := &carryingChannelSender{}
	d := NewDispatcher(e.store,
		fakeResolver{sender: &fakeSender{}, channel: channel, granted: []string{sendScope}},
		liveSeat(), &stubAttachments{ok: true, readErr: errors.New("the object store would not answer")},
		&stubConsent{}, nil,
		func() time.Time { return e.clockValue }, time.Hour, channelLadder)

	outcome, _, err := d.DispatchWithWait(e.ctx, id)
	if outcome != OutcomeRetry {
		t.Fatalf("outcome = %q (%v), want retry — nothing reached the provider, so the ladder may try again", outcome, err)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("the provider was handed %d message(s) after the file read failed", len(channel.sent))
	}
	// The marker is the falsehood this ordering removes: set here, the next
	// attempt would park saying the message may have been delivered.
	if at := e.inFlightAt(t, id); at != nil {
		t.Errorf("a delivery that never reached the provider carries an in-flight marker (%v), "+
			"so its next attempt would park as an unknown outcome", at)
	}
}
