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
