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
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// TestEveryCounterpartyShapeHasAReplyOriginArm walks the whole enum rather than
// the shapes a Counterparty can be built to produce: the point is what happens
// when someone ADDS a shape. Admission (Sink.Upsert) and this resolver each
// switch over it, and the two drifting apart is the failure — a shape admitted
// at the edge but unhandled here fails mid-transaction, after the activity and
// its audit row are written, which is a capture the connector retries forever.
//
// The bound is the enum's own shapeCount sentinel rather than a repeated list,
// so a shape appended to the const block joins this walk on its own and turns
// it red until the resolver names it. It asserts only that the resolver does
// not fall through to its unhandled arm; what each shape SHOULD answer is the
// sibling test's business.
func TestEveryCounterpartyShapeHasAReplyOriginArm(t *testing.T) {
	sink := &Sink{}
	for shape := shapeNone; shape < shapeCount; shape++ {
		_, _, err := sink.replyOriginForShape(context.Background(), nil, connector.Counterparty{}, shape)
		if err != nil && strings.Contains(err.Error(), "unhandled counterparty shape") {
			t.Errorf("shape %d falls through to the unhandled arm — admission lets it in, "+
				"so the reply path must answer for it rather than failing the capture", shape)
		}
	}
}

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
