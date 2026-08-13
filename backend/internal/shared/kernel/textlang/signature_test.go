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
	// A real notice, because the detector requires one: a lead phrase on a short
	// line is somebody writing a sentence, not a company's boilerplate.
	const lead = "This email and any attachments are confidential and intended solely for the addressee."

	if got := footerStart([]rune("Hallo,\n\nbis bald.\n\n" + lead)); got < 0 {
		t.Error("a footer opening its own line should be found")
	}
	mid := "I checked whether this email and any attachments are confidential and\nthey are not, which is a thing worth knowing before we send it onward."
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

// Two orderings the first version of this file got wrong, both found by review.
// Each is a case where a cut ran and the English stayed in anyway.
func TestTheEarliestBoundaryWins(t *testing.T) {
	const footer = "This email and any attachments are confidential and intended solely for the use of the addressee, and may not be disclosed or copied by anyone else."

	// A company that puts its legal notice ABOVE the signature block. Trusting
	// the sig-dash first cut at the LATER boundary and left the footer in.
	body := "Hallo Marek,\n\nvielen Dank für die Unterlagen, ich melde mich nächste Woche bei dir.\n\n" +
		footer + "\n\n-- \nLars Jankowfsky, Gradion"
	if got := Detect(body); got != German {
		t.Errorf("a footer above the sig-dash must still be cut: Detect = %q, want %q", got, German)
	}

	// A short reply, a signature, and then a quoted German thread. Cutting at
	// the sig-dash is what saves it: leaving the English signature in the pool
	// let it outvote the reply above it.
	quoted := "Hallo Marek, danke dir für die Unterlagen, ich schaue sie mir an.\n\n-- \n" +
		"Best regards from all of us here at the office, and do let us know\n" +
		"if there is anything further we can help you with at any time.\n\n" +
		"On Monday, Marek wrote:\n> Hallo Lars, anbei die Unterlagen zum Angebot.\n"
	if got := Detect(quoted); got != German {
		t.Errorf("an English signature must not outvote the reply: Detect = %q, want %q", got, German)
	}
}

// A lead phrase can open an ordinary sentence, and cutting there throws away
// the reply. The length floor is what separates the two, so it is pinned.
func TestAnOrdinarySentenceIsNotAFooter(t *testing.T) {
	prose := "The information contained in this proposal explains our position\n" +
		"on the dispatch integration, and I would welcome your thoughts on it\n" +
		"before we take it any further with the wider team."
	if got := footerStart([]rune(prose)); got >= 0 {
		t.Errorf("prose opening with a footer-ish phrase must not cut, got offset %d", got)
	}
	if got := Detect(prose); got != English {
		t.Errorf("and the message still reads as what it is: Detect = %q", got)
	}
}

// A reply can be shorter than the word floor and still be plainly one language.
// The floor counts words, which is the wrong question for a curt answer, so a
// ten-word German reply under an English signature used to keep the signature
// in the pool and come back English.
func TestACurtReplyUnderTheWordFloorIsStillRead(t *testing.T) {
	for _, reply := range []string{
		"Danke dir, das schaue ich mir an und melde mich.",
		"Ja, das passt so für mich, ich bin damit einverstanden.",
	} {
		// Under the floor, by construction: if these ever clear it the test has
		// stopped covering the branch it was written for.
		if got := WordsWritten(reply); got >= minReplyWords {
			t.Fatalf("this case must sit UNDER the word floor to be worth anything, got %d words", got)
		}
		body := reply + "\n\n-- \nBest regards from all of us here at the office,\n" +
			"and do let us know if there is anything further we can help with.\n"
		if got := Detect(body); got != German {
			t.Errorf("Detect(%q…) = %q, want %q", reply[:20], got, German)
		}
	}
}
