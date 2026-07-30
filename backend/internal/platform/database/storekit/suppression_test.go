// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

import "testing"

// The suppression hash is a matching contract between the eraser and
// every ingest path: normalization must be identical on both sides,
// and a pattern-metacharacter identifier must match itself, never act
// as a wildcard.

func TestSuppressionHashNormalizes(t *testing.T) {
	base := SuppressionHash("selma@example.test")
	for _, variant := range []string{"SELMA@example.test", "  selma@example.test  ", "Selma@Example.Test"} {
		if SuppressionHash(variant) != base {
			t.Errorf("variant %q hashes differently — a trivial respelling would resurrect the subject", variant)
		}
	}
	if SuppressionHash("other@example.test") == base {
		t.Error("distinct identifiers collide")
	}
}

// The channel key must separate the two fields it joins, and must separate
// providers: two accounts whose ids differ only in where the boundary falls
// are different humans, and the same numeric id on two providers is two
// people. A key that collided would suppress a stranger.
func TestChannelIdentityHashSeparatesProviderFromAccount(t *testing.T) {
	base := ChannelIdentityHash("telegram", "123456789")
	others := map[string]string{
		"telegram":   "12345678",  // a neighbouring account on the same provider
		"zalo":       "123456789", // the same digits on another provider
		"telegram:1": "23456789",  // the same characters, boundary shifted
	}
	for provider, channelUserID := range others {
		if ChannelIdentityHash(provider, channelUserID) == base {
			t.Errorf("%q/%q collides with telegram/123456789 — an erasure would suppress a different account",
				provider, channelUserID)
		}
	}
	if ChannelIdentityHash("Telegram", " 123456789 ") != base {
		t.Error("the channel key skips the normalization every suppression key shares")
	}
}

func TestEscapeLikeNeutralizesWildcards(t *testing.T) {
	cases := map[string]string{
		`a%b@example.test`: `a\%b@example.test`,
		`a_b@example.test`: `a\_b@example.test`,
		`a\b@example.test`: `a\\b@example.test`,
		`plain@example.te`: `plain@example.te`,
	}
	for in, want := range cases {
		if got := EscapeLike(in); got != want {
			t.Errorf("EscapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}
