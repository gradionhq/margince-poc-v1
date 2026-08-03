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

// TestEveryCounterpartyShapeIsAnsweredByBothSwitches walks the whole enum rather
// than the shapes a Counterparty can be built to produce, because the failure
// this guards is what happens when someone ADDS a shape.
//
// Two switches read it and they must not drift: admission
// (admitCounterpartyShape) decides whether the record is captured at all, and
// the reply resolver decides what medium its reply fact names. A shape admitted
// at the edge but unhandled in the resolver fails MID-TRANSACTION — after the
// activity, its audit row and its captured event are written — which the
// connector retries forever. Walking both in one test is what keeps the pair
// honest; covering only the resolver would leave the edge free to admit
// something nothing downstream can answer for.
//
// The bound is the enum's own shapeCount sentinel rather than a repeated list,
// so a shape appended to the const block joins this walk on its own.
func TestEveryCounterpartyShapeIsAnsweredByBothSwitches(t *testing.T) {
	const unhandled = "unhandled counterparty shape"
	sink := &Sink{}
	for shape := shapeNone; shape < shapeCount; shape++ {
		if err := admitCounterpartyShape(shape); err != nil && strings.Contains(err.Error(), unhandled) {
			t.Errorf("admission has no arm for shape %d — every shape is either captured or "+
				"refused by name, and neither can be decided by falling through", shape)
		}
		_, _, err := sink.replyOriginForShape(context.Background(), nil, connector.Counterparty{}, shape)
		if err != nil && strings.Contains(err.Error(), unhandled) {
			t.Errorf("the reply resolver has no arm for shape %d — admission lets it in, so the "+
				"resolver must answer for it rather than failing a capture already written", shape)
		}
	}
}

// TestAdmissionRefusesTheTwoMalformedShapesByName pins WHICH refusal each
// malformed shape earns. The walk above proves only that no shape falls
// through; a caller telling a record that names its human twice from one that
// names it by half a channel identity needs the sentinels to stay distinct.
func TestAdmissionRefusesTheTwoMalformedShapesByName(t *testing.T) {
	for _, tc := range []struct {
		shape counterpartyShape
		want  error
	}{
		{shapeNone, nil},
		{shapeMail, nil},
		{shapeChannel, nil},
		{shapeAmbiguous, ErrCounterpartyNamedTwice},
		{shapeHalfChannel, ErrChannelIdentityIncomplete},
	} {
		if err := admitCounterpartyShape(tc.shape); !errors.Is(err, tc.want) {
			t.Errorf("admitCounterpartyShape(%d) = %v, want %v", tc.shape, err, tc.want)
		}
	}
}

// TestAShapeOutsideTheEnumIsRefusedRatherThanAdmitted drives the arm the walk
// above cannot reach: a value no current constant names. Both switches must
// REFUSE it rather than fall through to their well-formed branch, because
// silently admitting a shape nothing can classify is how a capture reaches the
// middle of its transaction with no answer for what it is holding.
func TestAShapeOutsideTheEnumIsRefusedRatherThanAdmitted(t *testing.T) {
	const unhandled = "unhandled counterparty shape"
	unknown := shapeCount // past every named shape, by construction

	err := admitCounterpartyShape(unknown)
	if err == nil || !strings.Contains(err.Error(), unhandled) {
		t.Errorf("admission of an unnamed shape = %v, want it refused by name", err)
	}

	origin, ok, err := (&Sink{}).replyOriginForShape(context.Background(), nil, connector.Counterparty{}, unknown)
	if err == nil || !strings.Contains(err.Error(), unhandled) {
		t.Errorf("the reply resolver on an unnamed shape = %v, want it refused by name", err)
	}
	if ok || origin.channel != "" {
		t.Errorf("got origin %+v ok=%v, want neither — an unnamed shape names no medium", origin, ok)
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
