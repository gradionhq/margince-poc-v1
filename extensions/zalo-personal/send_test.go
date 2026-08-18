// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The transport half. Two properties carry most of this file:
//
//   - the unknown-outcome boundary, asserted in BOTH directions. Reporting an
//     ordinary refusal as unknown strands a message a human must chase; failing
//     to report a genuinely unanswered POST sends it twice, which nothing
//     downstream can detect and nobody can undo.
//   - Live answering from the row without spending the credential, asserted by
//     counting what it did NOT do.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

func TestSendTransmitsOnTheMemberSOwnSessionAndReturnsTheProviderSId(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.secrets.stored[callerUserID+"/"+sessionKey] = []byte(`{"cookies":[],"imei":"device-1"}`)
	live := &fakeSession{uid: "u-42", receipt: zaloReceipt{MsgID: "m-991"}}

	receipt, err := sendVia(context.Background(), rt, outbound("  hello  "), live.resume())
	if err != nil {
		t.Fatalf("sending: %v", err)
	}
	if receipt.ProviderMessageID != "m-991" {
		t.Fatalf("the receipt carries %q; it must be the provider's own message id", receipt.ProviderMessageID)
	}
	if len(live.sentTo) != 1 || live.sentTo[0] != "u-77" {
		t.Fatalf("the message went to %v; routing uses the account id and nothing else", live.sentTo)
	}
	if live.sent[0] != "hello" {
		t.Fatalf("the body left as %q", live.sent[0])
	}
}

// The boundary, both ways. It is one function on purpose, so this is the whole
// of the rule rather than a sample of it.
func TestOnlyAnUnansweredTransmissionIsReportedAsAnUnknownOutcome(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		cause   error
		unknown bool
	}{
		"the POST never answered": {cause: errUnanswered, unknown: true},
		"the POST never answered, wrapped by the protocol layer": {
			cause: errors.Join(errors.New("posting /api/message/sms"), errUnanswered), unknown: true,
		},
		"Zalo refused the message":   {cause: errors.New("error_code 114: cannot message this account")},
		"the recipient was rejected": {cause: errors.New("error_code 216: unknown recipient")},
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := newRuntime()
			rt.secrets.stored[callerUserID+"/"+sessionKey] = []byte(`{"cookies":[]}`)
			live := &fakeSession{uid: "u-42", sendErr: want.cause}

			_, err := sendVia(context.Background(), rt, outbound("hello"), live.resume())
			if got := errors.Is(err, extension.ErrSendOutcomeUnknown); got != want.unknown {
				t.Fatalf("%s reported unknown-outcome=%v, want %v (err: %v)", name, got, want.unknown, err)
			}
			if err == nil {
				t.Fatal("a refused transmission was reported as a delivery")
			}
		})
	}
}

// Everything BEFORE the transmission is an answer, however it failed: nothing
// went out, so the core is right to retry it. Reporting one as unknown parks a
// message that would have sent.
func TestAFailureBeforeTheTransmissionIsNeverAnUnknownOutcome(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*fakeRuntime, *fakeSession){
		"the session could not be resumed": func(rt *fakeRuntime, live *fakeSession) {
			rt.secrets.stored[callerUserID+"/"+sessionKey] = []byte(`{"cookies":[]}`)
			live.resumeErr = errors.New("the handshake was refused")
		},
		"the sealed session is unreadable": func(rt *fakeRuntime, _ *fakeSession) {
			rt.secrets.stored[callerUserID+"/"+sessionKey] = []byte(`not json`)
		},
		"the custodian could not be reached": func(rt *fakeRuntime, _ *fakeSession) {
			rt.secrets.getErr = errors.New("the custodian is unavailable")
		},
	}
	for name, arrange := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt, live := newRuntime(), &fakeSession{uid: "u-42"}
			arrange(rt, live)

			_, err := sendVia(context.Background(), rt, outbound("hello"), live.resume())
			if err == nil {
				t.Fatalf("%s was reported as a delivery", name)
			}
			if errors.Is(err, extension.ErrSendOutcomeUnknown) {
				t.Fatalf("%s was reported as unknown-outcome; nothing was transmitted, so the core may retry it", name)
			}
			if len(live.sent) != 0 {
				t.Fatal("a message was transmitted after a pre-flight failure")
			}
		})
	}
}

func TestSendRefusesAMemberWithNoSessionOnDeposit(t *testing.T) {
	t.Parallel()
	rt, live := newRuntime(), &fakeSession{uid: "u-42"}

	_, err := sendVia(context.Background(), rt, outbound("hello"), live.resume())
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("sending as a member who deposited nothing answered %v, want ErrForbidden", err)
	}
	if live.resumes != 0 {
		t.Fatal("a session was resumed for a member who has none")
	}
}

func TestSendRefusesADeliveryItCannotAddress(t *testing.T) {
	t.Parallel()
	tests := map[string]extension.OutboundMessage{
		"an empty body":       outbound("   "),
		"no recipient at all": {Member: callerUserID, Body: "hello"},
	}
	for name, msg := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := newRuntime()
			rt.secrets.stored[callerUserID+"/"+sessionKey] = []byte(`{"cookies":[]}`)
			live := &fakeSession{uid: "u-42"}

			if _, err := sendVia(context.Background(), rt, msg, live.resume()); !errors.Is(err, extension.ErrInvalid) {
				t.Fatalf("%s answered %v, want ErrInvalid", name, err)
			}
			if live.resumes != 0 {
				t.Fatal("the credential was spent on a delivery that could never be addressed")
			}
		})
	}
}

// Live must be answerable from state this side already holds. Counting the
// secret reads is the assertion: a pre-flight that unsealed would still return
// the right boolean, and would burn a session the member recovers with a phone.
func TestLiveAnswersFromTheRowWithoutSpendingTheCredential(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		row  []any
		none bool
		want bool
	}{
		"a working connection":           {row: connectionRow(statusConnected, "u-42", true), want: true},
		"a session that needs a re-scan": {row: connectionRow("needs_reconnect", "u-42", true)},
		"a member who disconnected":      {row: connectionRow(statusDisconnected, "u-42", false)},
		"a member who never connected":   {none: true},
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := newRuntime()
			if want.none {
				rt.tx.noRows = map[int]bool{1: true}
			} else {
				rt.tx.singleRows = [][]any{want.row}
			}
			// On deposit, so a Live that unsealed would succeed rather than
			// fail — the test has to make the wrong implementation PASS its
			// boolean and be caught by the count.
			rt.secrets.stored[callerUserID+"/"+sessionKey] = []byte(`{"cookies":[]}`)

			got, err := live(context.Background(), rt, callerUserID)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got != want.want {
				t.Fatalf("%s reads as live=%v, want %v", name, got, want.want)
			}
			if rt.secrets.gets != 0 {
				t.Fatalf("%s unsealed the credential %d times to answer a question the row holds", name, rt.secrets.gets)
			}
		})
	}
}

// An inability to tell is an ERROR and not a false: the core parks on false and
// retries on an error, so collapsing them strands a deliverable message.
func TestLiveReportsAnUnreadableRowRatherThanGuessing(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.txErr = errors.New("the transaction could not be opened")

	got, err := live(context.Background(), rt, callerUserID)
	if err == nil {
		t.Fatal("a connection nothing could read was reported as a confirmed answer")
	}
	if got {
		t.Fatal("an unreadable connection answered live")
	}
}

// outbound is one staged delivery, as the core hands it in.
func outbound(body string) extension.OutboundMessage {
	return extension.OutboundMessage{
		Member: callerUserID,
		Recipient: extension.ChannelIdentity{
			Provider: provider, ChannelUserID: "u-77", DisplayName: "A prospect",
		},
		Body:           body,
		IdempotencyKey: "delivery-1",
		Attempt:        1,
	}
}
