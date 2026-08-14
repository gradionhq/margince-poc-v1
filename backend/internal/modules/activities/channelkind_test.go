// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import "testing"

// The set is derived, not a fixed literal: register a kind nobody shipped
// with, and it is admitted; call SetChannelProviders again with a smaller
// set, and the kind dropped from it stops being admitted. A guard that only
// proves the fixed case (telegram is a channel) would pass even if the map
// went back to a hardcoded literal under the hood.
func TestIsChannelKindIsDerivedFromSetChannelProviders(t *testing.T) {
	defer SetChannelProviders(nil) // leave no cross-test residue

	SetChannelProviders([]string{"telegram", "fake-unit-provider"})
	if !IsChannelKind("fake-unit-provider") {
		t.Error("a provider just registered is not recognised as a channel kind")
	}

	SetChannelProviders([]string{"telegram"})
	if IsChannelKind("fake-unit-provider") {
		t.Error("a provider no longer registered is still recognised as a channel kind")
	}

	if !IsChannelKind("telegram") {
		t.Error("telegram, still registered, stopped being recognised")
	}
	if IsChannelKind("email") {
		t.Error("email is not a channel kind and must never be admitted")
	}
}
