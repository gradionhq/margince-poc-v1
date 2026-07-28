// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector_test

import (
	"context"
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
