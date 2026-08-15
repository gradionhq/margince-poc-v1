// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// A caller naming a kind that is also a registered transport is naming the
// transport, and the mapped input records it. Without this, a hand-logged or
// agent-logged channel activity would store no transport and be unrepliable —
// while an identical row written before the column existed stayed repliable,
// because the migration backfilled that one. Same data, different behaviour
// decided by write date.
//
// These are mirrored deliberately: the positive case alone would pass just as
// well if the mapping recorded the kind as a transport unconditionally, which
// would put 'note' and 'email' in a column that references the provider registry
// and fail the foreign key at the insert.

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestLogActivityInputRecordsTheTransportForAChannelKind(t *testing.T) {
	// Set explicitly rather than leaning on the package's pre-registry default:
	// what this asserts is a rule about the REGISTERED set, and a test that only
	// passes while a particular default happens to be in place is asserting the
	// default.
	t.Cleanup(func() { SetChannelProviders([]string{"telegram"}) })
	SetChannelProviders([]string{"telegram"})

	in, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{
		Kind: crmcontracts.CreateActivityRequestKindTelegram,
	})
	if err != nil {
		t.Fatalf("mapping a telegram activity: %v", err)
	}
	if in.ChannelProvider != "telegram" {
		t.Fatalf("ChannelProvider = %q, want telegram — a hand-logged channel activity that records no transport cannot be replied to", in.ChannelProvider)
	}
	if in.Kind != "telegram" {
		t.Fatalf("Kind = %q, want telegram: the transport is recorded ALONGSIDE the kind, never instead of it", in.Kind)
	}
}

func TestLogActivityInputRecordsNoTransportForANonChannelKind(t *testing.T) {
	t.Cleanup(func() { SetChannelProviders([]string{"telegram"}) })
	SetChannelProviders([]string{"telegram"})

	for _, kind := range []crmcontracts.CreateActivityRequestKind{
		crmcontracts.CreateActivityRequestKindNote,
		crmcontracts.CreateActivityRequestKindEmail,
		crmcontracts.CreateActivityRequestKindMeeting,
	} {
		in, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{Kind: kind})
		if err != nil {
			t.Fatalf("mapping a %s activity: %v", kind, err)
		}
		if in.ChannelProvider != "" {
			t.Errorf("kind %s recorded transport %q, want none — the column references the provider registry, "+
				"so a kind that names no transport must store NULL or fail the foreign key", kind, in.ChannelProvider)
		}
	}
}

// A provider that is not registered in THIS installation is not a transport here,
// whatever the contract's kind enum still admits. The registry is the authority,
// so an installation that composes no telegram connector records no telegram
// transport — and the row is refused by the foreign key rather than pointing at a
// provider nothing can deliver on.
func TestLogActivityInputRecordsNoTransportWhenTheProviderIsNotRegistered(t *testing.T) {
	t.Cleanup(func() { SetChannelProviders([]string{"telegram"}) })
	SetChannelProviders([]string{})

	in, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{
		Kind: crmcontracts.CreateActivityRequestKindTelegram,
	})
	if err != nil {
		t.Fatalf("mapping a telegram activity with no registered providers: %v", err)
	}
	if in.ChannelProvider != "" {
		t.Fatalf("ChannelProvider = %q with an empty registry, want none", in.ChannelProvider)
	}
}
