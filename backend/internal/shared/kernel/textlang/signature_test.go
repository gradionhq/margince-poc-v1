// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package textlang

import "testing"

// The defect this file exists for: a two-line German reply under a long English
// legal footer resolved to English, so a German thread got an English draft.
//
// The footer is the real thing — the phrasing a European company puts under
// every outbound mail — because a hand-shortened one would not carry enough
// English function words to reproduce the bug, and a test that cannot reproduce
// the bug cannot prove it is fixed.
func TestALegalFooterDoesNotOutvoteTheReplyAboveIt(t *testing.T) {
	const footer = `This email and any attachments are confidential and intended solely for the
use of the individual to whom they are addressed. If you are not the intended
recipient, you are notified that any disclosure, copying, distribution or the
taking of any action in reliance on the contents of this information is
strictly prohibited. If you have received this email in error, please notify
the sender immediately and delete it from your system. Please note that any
views or opinions presented in this email are solely those of the author and
do not necessarily represent those of the company.`

	cases := []struct {
		name string
		body string
		want Lang
	}{
		{
			name: "a short German reply under an English footer stays German",
			body: "Hallo Marek,\n\nvielen Dank für die Unterlagen. Ich schaue mir das an und melde mich\nnächste Woche bei dir mit einer Rückmeldung.\n\nViele Grüße\nLars\n\n" + footer,
			want: German,
		},
		{
			name: "the same reply with a sig-dash before the footer",
			body: "Hallo Marek,\n\nvielen Dank für die Unterlagen. Ich schaue mir das an und melde mich\nnächste Woche bei dir mit einer Rückmeldung.\n\n-- \nLars Jankowfsky\n\n" + footer,
			want: German,
		},
		{
			name: "an English reply under the same footer is still English",
			body: "Hi Marek,\n\nthanks for sending the documents over. I will read through them and get\nback to you next week with an answer.\n\nBest\nLars\n\n" + footer,
			want: English,
		},
		{
			name: "a message that is only the footer keeps reading it",
			body: footer,
			want: English,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Detect(c.body); got != c.want {
				t.Errorf("Detect = %q, want %q", got, c.want)
			}
		})
	}
}

// The sig-dash is matched exactly, because the near-misses are ordinary prose
// and cutting at them throws the reply away.
func TestTheSigDashIsMatchedExactlyAndNotByNearMisses(t *testing.T) {
	cases := []struct {
		name string
		body string
		cuts bool
	}{
		{name: "the RFC sig-dash with its trailing space", body: "line\n-- \nsig", cuts: true},
		{name: "the sig-dash without the trailing space", body: "line\n--\nsig", cuts: true},
		{name: "a horizontal rule is not a sig-dash", body: "line\n---\nmore", cuts: false},
		{name: "a person writing a dash before their name", body: "line\n-- Lars\nmore", cuts: false},
		{name: "a dash inside prose", body: "a line -- with a dash\nmore", cuts: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sigDashStart([]rune(c.body)) >= 0; got != c.cuts {
				t.Errorf("sigDashStart cuts = %v, want %v", got, c.cuts)
			}
		})
	}
}

// A footer lead is recognized where a footer actually starts — at the head of a
// line. The same words inside a sentence are somebody writing about a message,
// and cutting there would silently truncate their reply.
func TestAFooterLeadOnlyCutsAtTheStartOfALine(t *testing.T) {
	const lead = "This email and any attachments are confidential."

	if got := footerStart([]rune("Hallo,\n\nbis bald.\n\n" + lead)); got < 0 {
		t.Error("a footer opening its own line should be found")
	}
	mid := "I checked whether this email and any attachments are confidential and\nthey are not."
	if got := footerStart([]rune(mid)); got >= 0 {
		t.Errorf("the same phrase mid-sentence must not cut, got offset %d", got)
	}
	if got := footerStart([]rune("Hallo,\n\nbis bald.\n\n  " + lead)); got < 0 {
		t.Error("an indented footer is still a footer")
	}
}

// Both cuts share one floor, and the floor is what keeps them honest: text that
// is only a footer, or only a quote, is still the only evidence there is.
func TestNeitherCutIsAllowedToLeaveTooLittleToRead(t *testing.T) {
	// Nine words above the sig-dash, under the twelve-word floor: the greeting
	// is not a reply, so cutting here would answer Unknown on a message whose
	// signature block is the only German in it.
	short := []rune("Hi\n\n-- \nMit freundlichen Grüßen aus dem Büro in München, Ihr Team")
	if got := cutAt(short, sigDashStart(short)); len(got) != len(short) {
		t.Errorf("a nine-word lead-in must not be cut down to itself, kept %d of %d runes", len(got), len(short))
	}
	// The same shape with a real reply above it does cut.
	long := []rune("Hallo Marek, vielen Dank für die Unterlagen, ich melde mich nächste Woche bei dir.\n\n-- \nsignature here")
	if got := cutAt(long, sigDashStart(long)); len(got) >= len(long) {
		t.Error("a real reply above a sig-dash should be cut free of the signature")
	}
}
