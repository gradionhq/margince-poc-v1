// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The shapes replyOriginOf answers WITHOUT a database: a record naming nobody,
// and the two malformed shapes Upsert already refuses. Each returns before the
// contact lookup, which is what lets a nil tx stand here — and asserting that
// is the point, because a shape that reached the lookup with a nil tx would
// panic rather than refuse.
//
// The mail and channel arms both query, so they are proved in the integration
// lane (compose/integration) against a real Postgres.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func TestReplyOriginOf_RefusesMalformedAndUnnamedCounterparties(t *testing.T) {
	sink := &Sink{}
	for _, tc := range []struct {
		name    string
		cp      connector.Counterparty
		wantErr error
	}{
		{
			// A calendar event or an import carrying no counterparty at all: no
			// medium to answer on and nobody to answer, so no reply origin.
			name: "no counterparty names no origin",
			cp:   connector.Counterparty{},
		},
		{
			name: "an address and a channel identity together are refused",
			cp: connector.Counterparty{
				Email:           "alice@example.com",
				ChannelIdentity: connector.ChannelIdentity{Provider: "telegram", ChannelUserID: "77"},
			},
			wantErr: ErrCounterpartyNamedTwice,
		},
		{
			name:    "half a channel identity is refused",
			cp:      connector.Counterparty{ChannelIdentity: connector.ChannelIdentity{Provider: "telegram"}},
			wantErr: ErrChannelIdentityIncomplete,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin, ok, err := sink.replyOriginOf(context.Background(), nil, tc.cp)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
			if ok {
				t.Errorf("got ok=true, want false")
			}
			if origin.channel != "" {
				t.Errorf("got channel %q, want empty", origin.channel)
			}
			if origin.contactID != nil {
				t.Errorf("got contact %v, want nil", origin.contactID)
			}
		})
	}
}
