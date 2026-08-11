// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package textlang_test

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// The bug this package exists to fix: a German thread produced an English
// draft, because nothing anywhere asked what language the correspondence was
// in. Real correspondence, not sentences built to be detected.
func TestDetectsTheLanguageOfRealCorrespondence(t *testing.T) {
	cases := []struct {
		name string
		text string
		want textlang.Lang
	}{
		{
			name: "german business mail",
			text: "Hallo Herr Janetzke,\n\nvielen Dank für die Einführung. Ich würde mich gerne " +
				"kurz mit Ihnen austauschen, wenn Sie diese Woche noch Zeit haben. Bitte sagen " +
				"Sie mir, welcher Termin für Sie passt.\n\nViele Grüße",
			want: textlang.German,
		},
		{
			name: "english business mail",
			text: "Hi Marek,\n\nthanks for the introduction. I would like to have a short call " +
				"with you this week if you have the time. Please let me know which slot works " +
				"for you.\n\nBest regards",
			want: textlang.English,
		},
		{
			name: "vietnamese business mail",
			text: "Chào anh Minh,\n\ncảm ơn anh đã giới thiệu. Tôi muốn trao đổi ngắn với anh " +
				"trong tuần này nếu anh có thời gian. Anh cho tôi biết khung giờ nào phù hợp.\n\n" +
				"Trân trọng",
			want: textlang.Vietnamese,
		},
		{
			name: "german without any umlaut still resolves",
			text: "Hallo,\n\nwir haben das Angebot intern besprochen und wollen es so machen. " +
				"Ich melde mich bei Ihnen, sobald wir eine Entscheidung haben. Bitte geben Sie " +
				"mir noch Bescheid, ob das so passt.",
			want: textlang.German,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := textlang.Detect(c.text); got != c.want {
				t.Fatalf("Detect(%s) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// The reply-above-quote shape is the common one and the one a naive counter
// gets wrong: a short German reply sits on top of a long English thread, and
// the language being WRITTEN is the German at the top.
func TestAGermanReplyAboveAQuotedEnglishThreadIsGerman(t *testing.T) {
	reply := "Hallo Marek,\n\ndas passt gut. Ich würde vorschlagen, dass wir uns " +
		"nächste Woche kurz austauschen. Bitte sagen Sie mir, welcher Tag für Sie " +
		"funktioniert, dann schicke ich eine Einladung.\n\nViele Grüße\n\n"
	quoted := strings.Repeat(
		"> The team has reviewed the proposal and we would like to move forward with "+
			"it. Could you let us know what the next steps are on your side?\n", 12)

	if got := textlang.Detect(reply + quoted); got != textlang.German {
		t.Fatalf("Detect(german reply over english quote) = %q, want %q", got, textlang.German)
	}
}

// A captured email body carries its own envelope headers above the text
// somebody wrote. Those must not read as a quote marker, or the message is cut
// to nothing and its language resolves to Unknown.
//
// This is the shape that shipped the defect: a German thread whose stored body
// began "From: …" detected as Unknown, so the draft fell back to English.
func TestAMessagesOwnHeadersAreNotAQuote(t *testing.T) {
	stored := "From: marek.janetzke@lucidlabs.de\nTo: lars@gradion.com\n\n" +
		"Hallo zusammen,\n\nwie besprochen verbinde ich euch hiermit gerne direkt. " +
		"Romina, ich hatte dir Gradion für euren Case empfohlen und wollte fragen, " +
		"ob wir dazu kurz sprechen können.\n"

	if got := textlang.Detect(stored); got != textlang.German {
		t.Fatalf("Detect(captured german mail) = %q, want German: the envelope headers "+
			"the capture path stores are the message's own, not a quoted thread", got)
	}
}

// The same words further down DO introduce a quoted message, and there the cut
// is correct. Position is what separates the two.
func TestAnAddressHeaderBelowTheReplyStillCutsTheQuote(t *testing.T) {
	reply := "Hallo Marek,\n\ndas passt gut, ich melde mich bei Ihnen mit einer Antwort " +
		"und schicke die Unterlagen mit. Viele Grüße\n\n" +
		strings.Repeat("Ein weiterer Satz auf Deutsch, damit die Antwort lang genug ist.\n", 6)
	quoted := "From: marek.janetzke@lucidlabs.de\n\n" +
		strings.Repeat("The team has reviewed this and would like to move forward with it.\n", 12)

	if got := textlang.Detect(reply + quoted); got != textlang.German {
		t.Fatalf("Detect(german reply over a From:-quoted english thread) = %q, want German", got)
	}
}

// A message that is nothing but forwarded text still has a clear language, and
// refusing to read the quote would answer Unknown for it. The quote is dropped
// only when there is a reply above it to read instead.
func TestAForwardWithNoReplyAboveItIsReadFromTheQuote(t *testing.T) {
	forward := "> Hallo,\n> \n> wir haben die Unterlagen geprüft und würden gerne " +
		"weitermachen. Bitte sagen Sie uns, welche Schritte als nächstes bei Ihnen " +
		"anstehen und wann wir mit einer Antwort rechnen können.\n"

	if got := textlang.Detect(forward); got != textlang.German {
		t.Fatalf("Detect(quoted-only german) = %q, want German: with no reply above it "+
			"the quote is the only evidence there is", got)
	}
}

// Quote removal has to happen before the Vietnamese check, not after it. A
// quoted Vietnamese chain under an English reply raises the diacritic density
// of the whole input well past the threshold.
func TestQuotedTextIsRemovedBeforeVietnameseIsConsidered(t *testing.T) {
	reply := "Hi Minh,\n\nthat works for us. I will send the contract over tomorrow " +
		"and we can go from there.\n\n"
	quoted := strings.Repeat("> Tôi sẽ gửi đề xuất vào ngày mai và chờ phản hồi.\n", 10)

	if got := textlang.Detect(reply + quoted); got != textlang.English {
		t.Fatalf("Detect(english reply over vietnamese quote) = %q, want English", got)
	}
}

// The Vietnamese test is an explicit character set, not "any letter that is
// not plain ASCII". The loose predicate calls a French loanword Vietnamese.
func TestAccentedLoanwordsAreNotVietnamese(t *testing.T) {
	cases := []struct {
		name string
		text string
		want textlang.Lang
	}{
		{
			name: "french loanwords in english",
			text: "Please send José's résumé and the café invoice when you have a moment.",
			want: textlang.English,
		},
		{
			name: "a language this package does not know is Unknown, not Vietnamese",
			text: "Пожалуйста, отправьте документы на следующей неделе.",
			want: textlang.Unknown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := textlang.Detect(c.text); got != c.want {
				t.Fatalf("Detect(%s) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// "On" and "Am" open ordinary sentences as well as attribution lines, so an
// opener alone must not cut the message off at its second line.
func TestASentenceOpeningLikeAnAttributionLineIsNotAQuoteHeader(t *testing.T) {
	cases := []struct {
		name string
		text string
		want textlang.Lang
	}{
		{
			name: "english prose beginning On",
			text: "Hi team,\nOn balance we should review the proposal, because the terms " +
				"are clear and we have the time for it this week.",
			want: textlang.English,
		},
		{
			name: "german prose beginning Am",
			text: "Hallo,\nAm Montag besprechen wir das Angebot mit dem Team, und ich " +
				"melde mich danach bei Ihnen mit einer Antwort.",
			want: textlang.German,
		},
		{
			name: "a real attribution line does cut",
			text: "Hallo Marek,\n\ndas passt gut, ich melde mich bei Ihnen. Viele Grüße\n\n" +
				"On 3 June 2026, Marek wrote:\n" +
				strings.Repeat("The team has reviewed this and we would like to move "+
					"forward with the proposal as it stands.\n", 12),
			want: textlang.German,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := textlang.Detect(c.text); got != c.want {
				t.Fatalf("Detect(%s) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// Clients indent quoted lines and end lines in three different ways. A quote
// that announces itself must be found however it is laid out.
func TestQuotedLinesAreFoundWhateverTheIndentAndLineEnding(t *testing.T) {
	reply := "Hallo Marek,\n\ndas passt gut, ich melde mich bei Ihnen mit einer " +
		"Antwort. Viele Grüße\n\n"
	quote := strings.Repeat("   > The team has reviewed this and we would like to "+
		"move forward with the proposal.\n", 12)

	if got := textlang.Detect(reply + quote); got != textlang.German {
		t.Errorf("Detect(indented quote) = %q, want German", got)
	}

	crOnly := strings.ReplaceAll(reply+quote, "\n", "\r")
	if got := textlang.Detect(crOnly); got != textlang.German {
		t.Errorf("Detect(CR line endings) = %q, want German", got)
	}
}

// The honest answer for thin or genuinely mixed evidence. This bias is the
// design: a false German draft on an English thread is worse than the bug it
// replaces, and the caller has other tiers to fall back to.
func TestReportsUnknownRatherThanGuessing(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "empty", text: ""},
		{name: "one greeting", text: "Hallo"},
		{name: "too few hits to clear the floor", text: "Danke!"},
		{name: "no function words at all", text: "Roadmap Q3 2026 Kickoff Workshop Berlin"},
		{
			name: "genuinely mixed, neither language clearly ahead",
			text: "Hi, das ist the plan for uns: we haben a call and dann wir see.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := textlang.Detect(c.text); got != textlang.Unknown {
				t.Fatalf("Detect(%s) = %q, want Unknown", c.name, got)
			}
		})
	}
}

// Mutation check on MinHits: the floor is what stops a single shared word from
// deciding a language. Text that clears the floor by exactly one hit must
// resolve, and text one hit short of it must not - so lowering the constant
// changes an answer this test pins.
func TestTheHitFloorIsWhatSeparatesEvidenceFromNoise(t *testing.T) {
	belowFloor := "Der die."
	if got := textlang.Detect(belowFloor); got != textlang.Unknown {
		t.Fatalf("Detect(two german stopwords) = %q, want Unknown: below the %d-hit floor "+
			"nothing should resolve", got, textlang.MinHits)
	}

	atFloor := "Der die das und."
	if got := textlang.Detect(atFloor); got != textlang.German {
		t.Fatalf("Detect(four german stopwords) = %q, want German: clearing the %d-hit floor "+
			"with no english present should resolve", got, textlang.MinHits)
	}
}

// Mutation check on MinMargin: a clear lead is a language, a narrow one is a
// mixture. Both sides of the boundary are pinned, so widening or removing the
// margin flips one of them.
func TestALeadOnlyCountsWhenItIsClear(t *testing.T) {
	narrow := "Der die das und the a an and."
	if got := textlang.Detect(narrow); got != textlang.Unknown {
		t.Fatalf("Detect(four german, four english) = %q, want Unknown: neither side leads "+
			"by the %.1fx margin", got, textlang.MinMargin)
	}

	clear := "Der die das und nicht auch immer bitte the a."
	if got := textlang.Detect(clear); got != textlang.German {
		t.Fatalf("Detect(eight german, two english) = %q, want German: that is well past "+
			"the %.1fx margin", got, textlang.MinMargin)
	}
}

// German-only runes are worth a stopword hit because they are decisive on
// their own: no English or Vietnamese word carries them.
func TestUmlautsCountAsGermanEvidence(t *testing.T) {
	if got := textlang.Detect("Größe, Prüfung, Änderung."); got != textlang.German {
		t.Fatalf("Detect(three umlaut words) = %q, want German", got)
	}
}

// Vietnamese is decided by diacritic density, before stopwords are counted at
// all. The separation matters because its function words are short unaccented
// syllables that collide with both other languages.
func TestVietnameseIsDecidedByAccentDensityNotStopwords(t *testing.T) {
	if got := textlang.Detect("Tôi sẽ gửi cho anh bản đề xuất vào ngày mai."); got != textlang.Vietnamese {
		t.Fatalf("Detect(accented vietnamese) = %q, want Vietnamese", got)
	}

	// A German text carries umlauts, and must never cross the Vietnamese
	// threshold on them - the two accent sets are disjoint by construction.
	german := "Über die Prüfung der Größe möchte ich mit Ihnen sprechen, und zwar bald."
	if got := textlang.Detect(german); got != textlang.German {
		t.Fatalf("Detect(umlaut-heavy german) = %q, want German: umlauts are not "+
			"vietnamese diacritics", got)
	}
}
