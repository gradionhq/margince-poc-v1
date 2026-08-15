// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The kind and the transport are two axes, and the mapping holds them against
// each other in BOTH directions (ADR-0107/A158): a message names what carried
// it, and nothing else names anything.
//
// Both directions are asserted deliberately. The forward case alone would pass
// just as well if the mapping copied any provider it was handed onto any kind,
// which would put a transport on a note — a row the database then refuses with
// a CHECK the caller cannot see, reported as a 500 rather than as the field
// fault it is.
//
// The predecessor of this file tested the opposite rule: that a caller naming
// `kind: "telegram"` had that read back as a transport. That inference is gone,
// because the caller now has a field to say it in.

import (
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestLogActivityInputCarriesTheTransportTheCallerNamed(t *testing.T) {
	provider := "telegram"

	in, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{
		Kind:            crmcontracts.CreateActivityRequestKindMessage,
		ChannelProvider: &provider,
		Source:          "human",
	})
	if err != nil {
		t.Fatalf("mapping a message: %v", err)
	}
	if in.ChannelProvider != "telegram" {
		t.Fatalf("ChannelProvider = %q, want telegram — a message that records no transport cannot be replied to", in.ChannelProvider)
	}
	if in.Kind != KindMessage {
		t.Fatalf("Kind = %q, want %q: the transport is recorded ALONGSIDE the kind, never instead of it", in.Kind, KindMessage)
	}
}

// A message with no transport is refused HERE, as a field fault naming
// channel_provider, rather than reaching the database CHECK that enforces the
// same rule and surfacing as an unattributable 500.
func TestLogActivityInputRefusesAMessageWithNoTransport(t *testing.T) {
	_, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{
		Kind:   crmcontracts.CreateActivityRequestKindMessage,
		Source: "human",
	})

	var fault *MessageProviderError
	if !errors.As(err, &fault) {
		t.Fatalf("err = %v, want a MessageProviderError — a message with no transport must be a 422 naming the field, not a constraint violation", err)
	}
	if field, _, _ := fault.FieldFault(); field != "channel_provider" {
		t.Fatalf("FieldFault names %q, want channel_provider — the caller has to be told which field to fix", field)
	}
}

// The reverse direction, which is the one a test suite forgets: a kind that
// travelled on nothing must not acquire a transport. The column references the
// provider registry, so a note carrying one is both meaningless and a row the
// CHECK refuses.
func TestLogActivityInputRefusesATransportOnAKindThatTravelledOnNothing(t *testing.T) {
	provider := "telegram"

	for _, kind := range []crmcontracts.CreateActivityRequestKind{
		crmcontracts.CreateActivityRequestKindNote,
		crmcontracts.CreateActivityRequestKindEmail,
		crmcontracts.CreateActivityRequestKindMeeting,
		crmcontracts.CreateActivityRequestKindCall,
		crmcontracts.CreateActivityRequestKindTask,
	} {
		_, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{
			Kind:            kind,
			ChannelProvider: &provider,
			Source:          "human",
		})
		var fault *MessageProviderError
		if !errors.As(err, &fault) {
			t.Errorf("kind %s accepted a transport (err = %v), want a MessageProviderError — only a message travels on one", kind, err)
		}
	}
}

// Every other kind still maps cleanly when it names no transport, which is what
// stops the rule above from being satisfied by refusing everything.
func TestLogActivityInputAcceptsANonMessageWithNoTransport(t *testing.T) {
	for _, kind := range []crmcontracts.CreateActivityRequestKind{
		crmcontracts.CreateActivityRequestKindNote,
		crmcontracts.CreateActivityRequestKindEmail,
		crmcontracts.CreateActivityRequestKindMeeting,
	} {
		in, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{Kind: kind, Source: "human"})
		if err != nil {
			t.Fatalf("mapping a %s activity: %v", kind, err)
		}
		if in.ChannelProvider != "" {
			t.Errorf("kind %s recorded transport %q, want none", kind, in.ChannelProvider)
		}
	}
}
