// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import "testing"

// The two questions this module answers about a channel are DIFFERENT questions
// since ADR-0107/A158, and the split is the thing worth guarding: whether a row
// is a channel conversation at all is a fact about its kind, while whether a
// reply can leave this installation is a fact about the composed transport set.
//
// Collapsing them back into one — deriving IsChannelKind from the registry again
// — would make a message on a registered-but-uncomposed transport (whatsapp
// today) read as "not a conversation", which is the misreport the decision
// removed.
func TestIsChannelKindIsTheKindAndNotTheRegistry(t *testing.T) {
	// Restored to the pre-registry default, not nil/empty: later tests in this
	// package assume telegram is still composed, including on a t.Fatal — which
	// is why this is a defer rather than a final call.
	defer SetChannelProviders([]string{"telegram"})

	// Emptying the registry entirely must not change what a message IS.
	SetChannelProviders([]string{})
	if !IsChannelKind(KindMessage) {
		t.Errorf("%q stopped being a channel conversation when the registry emptied; the kind is a fact about the row, not about what this binary composed", KindMessage)
	}

	for _, kind := range []string{"email", "note", "task", "call", "meeting", "telegram"} {
		if IsChannelKind(kind) {
			t.Errorf("%q is treated as a channel conversation; only %q is one", kind, KindMessage)
		}
	}
}

// CanSendOnProvider is the half that IS derived: register a transport nobody
// shipped with and it becomes sendable; drop it and it stops. A guard that only
// proved the fixed case (telegram sends) would pass even if the map went back to
// a hardcoded literal underneath.
func TestCanSendOnProviderIsDerivedFromSetChannelProviders(t *testing.T) {
	defer SetChannelProviders([]string{"telegram"})

	SetChannelProviders([]string{"telegram", "fake-unit-provider"})
	if !CanSendOnProvider("fake-unit-provider") {
		t.Error("a transport just registered cannot be sent on")
	}

	SetChannelProviders([]string{"telegram"})
	if CanSendOnProvider("fake-unit-provider") {
		t.Error("a transport no longer registered can still be sent on")
	}
	if !CanSendOnProvider("telegram") {
		t.Error("telegram, still registered, stopped being sendable")
	}
}
