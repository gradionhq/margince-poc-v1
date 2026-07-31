// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector_test

// Recipient's one-of-two invariant. It is tested rather than trusted because
// the failure is silent in the direction that matters: a half-populated
// recipient reaching a default-deny suppression gate is asked about nobody and
// therefore refuses nobody, so the gate goes on passing while covering nothing.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func TestRecipientCarriesExactlyOneOfEmailOrChannel(t *testing.T) {
	channel := connector.ChannelIdentity{Provider: "telegram", ChannelUserID: "7788", Username: "buyer"}

	for _, tc := range []struct {
		name string
		r    connector.Recipient
		ok   bool
	}{
		{"a mail address alone", connector.Recipient{Email: "buyer@example.com"}, true},
		{"a channel identity alone", connector.Recipient{Channel: &channel}, true},
		{"both arms", connector.Recipient{Email: "buyer@example.com", Channel: &channel}, false},
		{"neither arm", connector.Recipient{}, false},
		{
			"a channel identity with no account id",
			connector.Recipient{Channel: &connector.ChannelIdentity{Provider: "telegram"}},
			false,
		},
		{
			"a channel identity with no provider",
			connector.Recipient{Channel: &connector.ChannelIdentity{ChannelUserID: "7788"}},
			false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.r.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want a usable recipient", err)
			}
			if !tc.ok && !errors.Is(err, connector.ErrRecipientShape) {
				t.Fatalf("Validate() = %v, want ErrRecipientShape", err)
			}
		})
	}
}

// The lift is what every mail caller uses, so a blank address must not become a
// recipient that names nobody and then passes the gate's own validation.
func TestEmailRecipientsRefuseNothingButLiftEveryAddress(t *testing.T) {
	got := connector.EmailRecipients([]string{"a@example.com", "b@example.com"})
	if len(got) != 2 {
		t.Fatalf("EmailRecipients lifted %d recipients, want 2", len(got))
	}
	for _, r := range got {
		if err := r.Validate(); err != nil {
			t.Fatalf("a lifted address failed validation: %v", err)
		}
	}
	if err := connector.EmailRecipients([]string{""})[0].Validate(); !errors.Is(err, connector.ErrRecipientShape) {
		t.Fatalf("a lifted blank address validated as a recipient: %v", err)
	}
}
