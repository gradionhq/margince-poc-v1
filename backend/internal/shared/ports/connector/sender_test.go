// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector_test

import (
	"context"
	"errors"
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
// whitespace and no brackets (those belong to the wire rendering alone).
func TestOutboundMessageValidateAcceptsOnlyASearchableIdentity(t *testing.T) {
	for _, tc := range []struct {
		id string
		ok bool
	}{
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
