// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package draftcheck_test

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/draftcheck"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// The draft the certification judge floored, and what this package exists to
// catch: after 241 days of silence the model still reached for "checking in"
// and "we discussed", with the ban written plainly in the system prompt.
func TestTheDraftTheJudgeFlooredIsCaught(t *testing.T) {
	body := "Hello Priya, I am checking in to see if you have an update regarding " +
		"the integration project we discussed earlier. Is this still a priority?"

	findings := draftcheck.Body(body, textlang.English, convstate.BandMonths)
	if len(findings) == 0 {
		t.Fatal("the phrasing the judge floored passed the check")
	}
	for _, f := range findings {
		if f.Rule != "assumed-memory" {
			t.Errorf("unexpected rule %q for %q", f.Rule, f.Phrase)
		}
	}
}

// The same words are FINE in a live exchange. A drafter answering this morning's
// message may say "as discussed", because the discussion is what both sides are
// still holding — which is why the check reads the band rather than a word list.
func TestTheSameWordsAreFineWhileTheExchangeIsLive(t *testing.T) {
	body := "Hi Marek, as discussed I am sending the scope over now. Anything else?"

	if findings := draftcheck.Body(body, textlang.English, convstate.BandFresh); len(findings) != 0 {
		t.Fatalf("a live exchange may refer to what was discussed, got %d findings: %+v",
			len(findings), findings)
	}
	if findings := draftcheck.Body(body, textlang.English, convstate.BandMonths); len(findings) == 0 {
		t.Fatal("the same sentence after months of silence should be caught")
	}
}

// A wellbeing opener is filler at every band, so it is not gated on one.
func TestAWellbeingOpenerIsCaughtAtEveryBand(t *testing.T) {
	body := "Hi Priya, I hope you are doing well. The scope document is attached."

	for _, band := range []convstate.Band{
		convstate.BandNone, convstate.BandFresh, convstate.BandWeeks, convstate.BandMonths,
	} {
		findings := draftcheck.Body(body, textlang.English, band)
		if len(findings) != 1 || findings[0].Rule != "wellbeing-opener" {
			t.Errorf("at band %q: got %+v, want one wellbeing-opener finding", band, findings)
		}
	}
}

// German and Vietnamese carry their own phrases; a German draft is judged
// against German reflexes, not translated English ones.
func TestEachLanguageIsJudgedAgainstItsOwnPhrases(t *testing.T) {
	german := "Hallo Marek, wie besprochen melde ich mich nochmal zu dem Thema."
	if findings := draftcheck.Body(german, textlang.German, convstate.BandMonths); len(findings) == 0 {
		t.Error("the German assumed-memory phrase should be caught")
	}
	// The same German text judged as English finds nothing, which is correct:
	// the caller passes the language the draft was written in.
	if findings := draftcheck.Body(german, textlang.English, convstate.BandMonths); len(findings) != 0 {
		t.Errorf("German text judged as English should find nothing, got %+v", findings)
	}
}

// A clean draft is clean. The check must not fire on ordinary correspondence,
// or the retry runs on every draft and buys nothing.
func TestAnHonestDraftPassesCleanly(t *testing.T) {
	body := "Hallo Marek,\n\nunser letzter Kontakt liegt lange zurück. Wir haben die " +
		"Schnittstelle inzwischen fertiggestellt und ich wollte fragen, ob das Thema " +
		"bei Ihnen noch aktuell ist.\n\nViele Grüße"

	if findings := draftcheck.Body(body, textlang.German, convstate.BandMonths); len(findings) != 0 {
		t.Fatalf("an honest gap-acknowledging draft should pass, got %+v", findings)
	}
}

// The feedback names the phrase and why it is wrong, because a model told only
// "try again" produces the same draft with different adjectives.
func TestFeedbackNamesThePhraseAndTheReason(t *testing.T) {
	findings := draftcheck.Body("I am just circling back on this.",
		textlang.English, convstate.BandMonths)
	feedback := draftcheck.Feedback(findings)

	if !strings.Contains(feedback, "circling back") {
		t.Errorf("the feedback should quote the phrase, got %q", feedback)
	}
	if !strings.Contains(feedback, "months") {
		t.Errorf("the feedback should say why it is wrong here, got %q", feedback)
	}
	if draftcheck.Feedback(nil) != "" {
		t.Error("no findings should produce no feedback")
	}
}

// A phrase must match as whole words. "our solution" sits inside "your
// solution", so a plain substring test flags an honest question about the
// recipient's OWN system as an invented pitch.
func TestAPhraseInsideAnotherWordIsNotAMatch(t *testing.T) {
	honest := "Hallo Marek, wie ist your solution bei Ihnen aufgebaut?"
	if findings := draftcheck.Body(honest, textlang.English, convstate.BandNone); len(findings) != 0 {
		t.Errorf("%q should not match \"our solution\", got %+v", honest, findings)
	}

	invented := "Hi Marek, our solution helps companies like yours."
	if findings := draftcheck.Body(invented, textlang.English, convstate.BandNone); len(findings) == 0 {
		t.Error("the real phrase should still be caught")
	}
}

// The wellbeing rule reads the opening only: "I hope that works for you" as a
// closing line is an ordinary sentence.
func TestAPleasantryIsOnlyFillerAtTheOpening(t *testing.T) {
	closing := "Hi Priya,\n\nThe integration scope is attached and the timeline is in " +
		"section three. It sets out the two phases and what each one needs from your " +
		"side, including the test window we talked through.\n\n" +
		"Let me know if the dates work. I hope you are doing well with the rollout."

	if findings := draftcheck.Body(closing, textlang.English, convstate.BandFresh); len(findings) != 0 {
		t.Errorf("a pleasantry far into the body is not an opener, got %+v", findings)
	}
}

// The chip that reached a real draft on the real Marek thread while the body
// beside it was correct:
//
//	"Follow-up to previous introduction by Romina Medici"
//
// Romina did not make that introduction. The product holds no person-to-person
// referral record at all, so any directed introduction fact in a draft was read
// out of quoted correspondence — which is how the reported defect got the
// direction backwards in the first place.
func TestAnInventedIntroductionInAChipIsCaught(t *testing.T) {
	labels := []string{"Follow-up to previous introduction by Romina Medici"}

	findings := draftcheck.Reasoning(labels, textlang.English, convstate.BandFresh)
	if len(findings) == 0 {
		t.Fatal("the chip that shipped the original defect passed the check")
	}
	if findings[0].Rule != "invented-relationship" {
		t.Errorf("expected an invented-relationship finding, got %q", findings[0].Rule)
	}
}

// A chip is the product explaining itself, and the phrase lists that judge the
// body apply to it too — an invented pitch is an invented pitch wherever it is
// shown.
func TestAChipIsJudgedByTheSamePhraseListsAsTheBody(t *testing.T) {
	labels := []string{"our solution for their dispatch problem"}

	if findings := draftcheck.Reasoning(labels, textlang.English, convstate.BandNone); len(findings) == 0 {
		t.Error("an invented pitch in a chip should be caught the way one in the body is")
	}
}

// An honest chip passes. The check must not fire on ordinary provenance, or
// every draft retries and the retry buys nothing.
func TestHonestChipsPassCleanly(t *testing.T) {
	labels := []string{"pricing concern", "asked about onboarding", "Angebot vom 25. Juli"}

	if findings := draftcheck.Reasoning(labels, textlang.German, convstate.BandWeeks); len(findings) != 0 {
		t.Errorf("ordinary provenance should pass, got %+v", findings)
	}
}

// German and Vietnamese carry their own phrasings, so a German chip is judged
// against German words rather than translated English ones.
func TestAnInventedIntroductionIsCaughtInEveryLanguage(t *testing.T) {
	for lang, label := range map[textlang.Lang]string{
		textlang.German:     "Nachfassen zur Vorstellung durch Romina Medici",
		textlang.Vietnamese: "Tiếp theo sau khi được giới thiệu bởi Romina",
	} {
		if findings := draftcheck.Reasoning([]string{label}, lang, convstate.BandFresh); len(findings) == 0 {
			t.Errorf("%s: %q was not caught", lang, label)
		}
	}
}

// The grammar around the noun is not predictable, so the noun is the refusal.
// A first attempt enumerated "introduction by" and "introduced by"; the model
// wrote "introduction TO" and walked straight through, twice, on a live stack.
func TestTheIntroductionNounIsCaughtInAnyGrammar(t *testing.T) {
	seen := []string{
		"follow up on introduction to Romina Medici (ERGO)",
		"follow-up on previous introduction",
		"Follow-up to previous introduction by Romina Medici",
		"intro made last month",
		"referral from a colleague",
	}
	for _, label := range seen {
		if findings := draftcheck.Reasoning([]string{label}, textlang.English, convstate.BandFresh); len(findings) == 0 {
			t.Errorf("%q was not caught", label)
		}
	}
}

// A chip is written for the rep, not the recipient, and the model reaches for
// English there even under German prose. "shared contact introduction" appeared
// on a live stack beside a German body, and a German-only list did not see it.
func TestAChipIsCheckedAgainstEveryLanguage(t *testing.T) {
	if findings := draftcheck.Reasoning([]string{"shared contact introduction"},
		textlang.German, convstate.BandFresh); len(findings) == 0 {
		t.Error("an English chip on a German draft should still be caught")
	}
	if findings := draftcheck.Reasoning([]string{"Vorstellung durch einen Kollegen"},
		textlang.English, convstate.BandFresh); len(findings) == 0 {
		t.Error("a German chip on an English draft should still be caught")
	}
}

// Every form the model has actually produced on a live stack, plus the ones
// enumerating word forms kept missing. The stem is what generalizes: matching
// "introduction by" missed "introduction to", and matching the noun missed
// "introductory".
func TestEveryFormOfAnInventedIntroductionIsCaught(t *testing.T) {
	seen := []string{
		"Follow-up to previous introduction by Romina Medici",
		"follow up on introduction to Romina Medici (ERGO)",
		"follow-up on previous introduction",
		"introductory connection to Romina Medici",
		"shared contact introduction",
		"introducing us to the team",
		"referral from a colleague",
		"Vorstellung durch einen Kollegen",
		"vorgestellt von Marek",
	}
	for _, label := range seen {
		if findings := draftcheck.Reasoning([]string{label}, textlang.English, convstate.BandFresh); len(findings) == 0 {
			t.Errorf("%q was not caught", label)
		}
	}
}

// The stem must not fire on unrelated words that merely contain those letters.
func TestTheStemDoesNotFireOnUnrelatedWords(t *testing.T) {
	honest := []string{
		"pricing concern", "asked about onboarding", "Angebot vom 25. Juli",
		"deferred the decision", "preferred delivery window",
	}
	if findings := draftcheck.Reasoning(honest, textlang.English, convstate.BandFresh); len(findings) != 0 {
		t.Errorf("ordinary provenance was flagged: %+v", findings)
	}
}

// German compounds the model produced on a live stack. A stem requiring a
// trailing space saw none of them.
func TestGermanCompoundsCarryingTheStemAreCaught(t *testing.T) {
	seen := []string{
		"Folge-Email nach Intro",
		"Folgekontakt zum Intro-Thema",
		"Folgekontakt nach Intro",
	}
	for _, label := range seen {
		if findings := draftcheck.Reasoning([]string{label}, textlang.German, convstate.BandFresh); len(findings) == 0 {
			t.Errorf("%q was not caught", label)
		}
	}
}
