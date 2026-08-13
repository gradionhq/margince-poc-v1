// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package textlang

import "testing"

// What a draft may stand on is what our correspondent WROTE. Everything else in
// a stored body — the envelope, a quoted thread, a forwarded original — is text
// somebody else wrote and this message merely carried, and grounding a draft in
// it puts words in the correspondent's mouth and their contacts' addresses in
// the prompt.
func TestMessageOpeningKeepsOnlyWhatTheSenderWrote(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"a real message survives with its envelope removed": {
			body: "From: marine@surfe.com\nTo: rep@gradion.com\n\nWe are comparing three providers.",
			want: "We are comparing three providers.",
		},
		"a reply keeps its own words and drops the quote": {
			body: "Thanks, that works.\n\n-----Original Message-----\nFrom: x@y.com\n\nolder text",
			want: "Thanks, that works.",
		},
		"a forward carries nobody's words of its own": {
			body: "-----Original Message-----\nFrom: someone@x.com\nTo: rep@y.com\n\nThe original body.",
			want: "",
		},
		"a German forward is the same message in another client": {
			body: "-----Ursprüngliche Nachricht-----\nVon: x@y.com\n\nAlter Text",
			want: "",
		},
		"a quoted-only body adds nothing": {
			body: "> quoted\n> back",
			want: "",
		},
		"an attachment-only mail is stored as its addresses alone": {
			body: "From: marine@surfe.com\nTo: rep@gradion.com",
			want: "",
		},
		"the same mail with a trailing newline reads the same": {
			body: "From: marine@surfe.com\nTo: rep@gradion.com\n",
			want: "",
		},
		// The envelope and a paragraph opening "From: our finance team's
		// perspective" have the same shape. What follows the colon tells them
		// apart: capture writes an address there, prose writes words.
		"prose shaped like a header is prose": {
			body: "From: our finance team's perspective this is fine.",
			want: "From: our finance team's perspective this is fine.",
		},
		"a two-line prose block shaped like an envelope survives": {
			body: "From: our finance team's perspective the timing is tight.\n" +
				"To: make this work we would need the scope by Friday.",
			want: "From: our finance team's perspective the timing is tight.\n" +
				"To: make this work we would need the scope by Friday.",
		},
		"capture's dash for a missing address is still an envelope": {
			body: "From: marine@surfe.com\nTo: -",
			want: "",
		},
		"an empty body is empty": {body: "", want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := MessageOpening(tc.body, 400); got != tc.want {
				t.Fatalf("MessageOpening(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// The bound is the point of asking for an opening. A caller that asks for a
// bounded snippet and receives the whole message would put the entire
// correspondence in the prompt, so a bound that cannot be honoured yields
// nothing rather than everything.
func TestMessageOpeningHonoursItsBound(t *testing.T) {
	body := "From: x@y.com\nTo: z@y.com\n\nDie Verhandlung über die Lieferbedingungen läuft weiter."

	if got := MessageOpening(body, 12); got != "Die Verhandl" {
		t.Fatalf("expected the opening cut at 12 runes, got %q", got)
	}
	if got := MessageOpening(body, 0); got != "" {
		t.Fatalf("a non-positive bound must yield nothing, got %q", got)
	}
	if got := MessageOpening(body, -1); got != "" {
		t.Fatalf("a negative bound must yield nothing, got %q", got)
	}
}

// Cutting by bytes would split a multi-byte character in half and hand the
// model a replacement rune where a word should be.
func TestMessageOpeningCutsOnRuneBoundaries(t *testing.T) {
	got := MessageOpening("Größenänderung überall", 5)
	if got != "Größe" {
		t.Fatalf("expected a clean rune cut, got %q", got)
	}
}
