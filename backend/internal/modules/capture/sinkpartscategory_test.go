// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import "testing"

// How a message arrived decides what kind of file came with it, and capture
// DERIVES that from the record rather than letting a connector declare it: a
// connector-supplied category is a string an untrusted producer can get wrong on
// a column that reaches the document library, the audit image and every category
// filter — and the record already says how it arrived.
func TestFileCategoryFollowsHowTheMessageArrived(t *testing.T) {
	for _, c := range []struct {
		name   string
		fields ActivityFields
		want   string
	}{
		{"a mail capture", ActivityFields{Kind: "email"}, CategoryEmailAttachment},
		{"a telegram message", ActivityFields{Kind: "message", ChannelProvider: "telegram"}, CategoryMessageAttachment},
		{"a zalo message", ActivityFields{Kind: "message", ChannelProvider: "zalo_oa"}, CategoryMessageAttachment},
		// A provider nobody has written an adapter for yet still arrived on a
		// channel. The derivation asks WHETHER a transport was named, not which
		// one — so a unit added tomorrow needs no edit here, and cannot fall
		// through to being called mail.
		{"an unrecognised provider is still not mail", ActivityFields{Kind: "message", ChannelProvider: "some_future_unit"}, CategoryMessageAttachment},
		// The third arm, and the reason it exists. A calendar capture files a
		// meeting with no transport; reading "no provider" as mail would label
		// its file an email attachment the moment gcal starts carrying files.
		{"a meeting arrived on neither", ActivityFields{Kind: "meeting"}, CategoryOtherAttachment},
		// The offline demo connector files whatever kind its fixture names, so
		// this is not a hypothetical shape either.
		{"a note arrived on neither", ActivityFields{Kind: "note"}, CategoryOtherAttachment},
	} {
		if got := fileCategoryFor(c.fields); got != c.want {
			t.Errorf("%s: category %q, want %q", c.name, got, c.want)
		}
	}
}

// The kind of interaction and the transport answer separate questions, and they
// were one column for as long as every channel was also a kind. Collapsing them
// again is the tempting simplification, and each direction is wrong in its own
// way: a `message` that named no transport is hand-logged, not a channel
// capture, and a kind is no evidence about a provider that IS named.
func TestKindAndTransportAreReadSeparately(t *testing.T) {
	if got := fileCategoryFor(ActivityFields{Kind: "message"}); got != CategoryOtherAttachment {
		t.Errorf("a message kind with no transport: category %q, want %q", got, CategoryOtherAttachment)
	}
	if got := fileCategoryFor(ActivityFields{Kind: "email", ChannelProvider: "telegram"}); got != CategoryMessageAttachment {
		t.Errorf("an email kind on a channel: category %q, want %q", got, CategoryMessageAttachment)
	}
}
