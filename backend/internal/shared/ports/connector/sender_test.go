// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

type stubSender struct{ got connector.OutboundMessage }

func (s *stubSender) Send(_ context.Context, _ connector.Auth, msg connector.OutboundMessage) (connector.SendReceipt, error) {
	s.got = msg
	return connector.SendReceipt{ProviderMessageID: "m1"}, nil
}

func TestSenderIsSatisfiedIndependentlyOfConnector(t *testing.T) {
	var s connector.Sender = &stubSender{}
	got, err := s.Send(context.Background(), connector.Auth("cred"), connector.OutboundMessage{
		To: []string{"buyer@example.com"}, Subject: "Re: pricing",
		Body: "As discussed.", MessageID: "abc@margince.test",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.ProviderMessageID != "m1" {
		t.Errorf("receipt = %+v, want m1", got)
	}
}

// A send-capable provider and a capture-capable provider are independent
// capabilities; the seam must not force one to imply the other.
func TestSenderDoesNotImplyConnector(t *testing.T) {
	var s connector.Sender = &stubSender{}
	if _, ok := s.(connector.Connector); ok {
		t.Error("stubSender satisfies Connector; the Sender seam must stand alone")
	}
}

// The seam's idempotency contract rests on a message identity a provider can
// search for. Validate is the precondition every implementation checks before
// provider I/O, so the shape it accepts is part of the port, not of one
// connector: unbracketed addr-spec, exactly one '@', both sides present, no
// whitespace, angle brackets, or ASCII control character (those belong to the
// wire rendering alone, or to nothing usable at all), and short enough for a
// header line to carry.
//
// The length bound is not cosmetic. The same predicate judges an identity READ
// BACK out of a provider response, which is remote input measured in megabytes,
// and whatever it accepts becomes a natural key and a thread key.
func TestOutboundMessageValidateAcceptsOnlyASearchableIdentity(t *testing.T) {
	const suffix = "@margince.test"
	for _, tc := range []struct {
		id string
		ok bool
	}{
		{strings.Repeat("a", 400) + suffix, true},              // long, still a header line
		{strings.Repeat("a", 513-len(suffix)) + suffix, false}, // one octet past the bound
		{strings.Repeat("a", 100_000) + suffix, false},         // a runaway read-back
		{"abc@margince.test", true},
		{"a.b+c@sub.margince.test", true},
		{"", false},
		{"abc", false},
		{"@margince.test", false},
		{"abc@", false},
		{"a@b@margince.test", false},
		{"<abc@margince.test>", false},
		{"abc @margince.test", false},
		{"abc@margince.test\r\nBcc: attacker@evil.test", false},
		{"abc@margince.test\x00", false}, // NUL — a control byte outside the originally rejected " \t\r\n<>" set
		{"abc@margince.test\x1f", false}, // unit separator — the last C0 control
		{"abc@margince.test\x7f", false}, // DEL
		{"ab\x0bc@margince.test", false}, // vertical tab, mid local-part rather than trailing
	} {
		err := connector.OutboundMessage{MessageID: tc.id}.Validate()
		if tc.ok && err != nil {
			t.Errorf("Validate(%q) = %v, want accepted", tc.id, err)
		}
		if !tc.ok && !errors.Is(err, connector.ErrInvalidMessageID) {
			t.Errorf("Validate(%q) = %v, want ErrInvalidMessageID", tc.id, err)
		}
	}
}
