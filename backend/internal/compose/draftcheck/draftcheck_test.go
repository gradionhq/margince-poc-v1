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

	findings := draftcheck.Body(body, textlang.English, convstate.BandMonths, false)
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

	if findings := draftcheck.Body(body, textlang.English, convstate.BandFresh, false); len(findings) != 0 {
		t.Fatalf("a live exchange may refer to what was discussed, got %d findings: %+v",
			len(findings), findings)
	}
	if findings := draftcheck.Body(body, textlang.English, convstate.BandMonths, false); len(findings) == 0 {
		t.Fatal("the same sentence after months of silence should be caught")
	}
}

// A wellbeing opener is filler at every band, so it is not gated on one.
func TestAWellbeingOpenerIsCaughtAtEveryBand(t *testing.T) {
	body := "Hi Priya, I hope you are doing well. The scope document is attached."

	for _, band := range []convstate.Band{
		convstate.BandNone, convstate.BandFresh, convstate.BandWeeks, convstate.BandMonths,
	} {
		findings := draftcheck.Body(body, textlang.English, band, false)
		if len(findings) != 1 || findings[0].Rule != "wellbeing-opener" {
			t.Errorf("at band %q: got %+v, want one wellbeing-opener finding", band, findings)
		}
	}
}

// German and Vietnamese carry their own phrases; a German draft is judged
// against German reflexes, not translated English ones.
func TestEachLanguageIsJudgedAgainstItsOwnPhrases(t *testing.T) {
	german := "Hallo Marek, wie besprochen melde ich mich nochmal zu dem Thema."
	if findings := draftcheck.Body(german, textlang.German, convstate.BandMonths, false); len(findings) == 0 {
		t.Error("the German assumed-memory phrase should be caught")
	}
	// The same German text judged as English finds nothing, which is correct:
	// the caller passes the language the draft was written in.
	if findings := draftcheck.Body(german, textlang.English, convstate.BandMonths, false); len(findings) != 0 {
		t.Errorf("German text judged as English should find nothing, got %+v", findings)
	}
}

// A clean draft is clean. The check must not fire on ordinary correspondence,
// or the retry runs on every draft and buys nothing.
func TestAnHonestDraftPassesCleanly(t *testing.T) {
	body := "Hallo Marek,\n\nunser letzter Kontakt liegt lange zurück. Wir haben die " +
		"Schnittstelle inzwischen fertiggestellt und ich wollte fragen, ob das Thema " +
		"bei Ihnen noch aktuell ist.\n\nViele Grüße"

	if findings := draftcheck.Body(body, textlang.German, convstate.BandMonths, false); len(findings) != 0 {
		t.Fatalf("an honest gap-acknowledging draft should pass, got %+v", findings)
	}
}

// The feedback names the phrase and why it is wrong, because a model told only
// "try again" produces the same draft with different adjectives.
func TestFeedbackNamesThePhraseAndTheReason(t *testing.T) {
	findings := draftcheck.Body("I am just circling back on this.",
		textlang.English, convstate.BandMonths, false)
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
	if findings := draftcheck.Body(honest, textlang.English, convstate.BandNone, false); len(findings) != 0 {
		t.Errorf("%q should not match \"our solution\", got %+v", honest, findings)
	}

	invented := "Hi Marek, our solution helps companies like yours."
	if findings := draftcheck.Body(invented, textlang.English, convstate.BandNone, false); len(findings) == 0 {
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

	if findings := draftcheck.Body(closing, textlang.English, convstate.BandFresh, false); len(findings) != 0 {
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

// A German draft that opens formally and closes familiarly reads as
// machine-written whichever register it should have picked. The prompt already
// said to be consistent; three consecutive drafts to one person came back du,
// du, Sie — which is why it is checked rather than merely instructed.
func TestAMixedRegisterIsCaught(t *testing.T) {
	mixed := "Hallo Frank,\n\nich würde mich gerne mit dir austauschen. " +
		"Haben Sie in der kommenden Woche Zeit für ein kurzes Gespräch?"

	findings := draftcheck.Body(mixed, textlang.German, convstate.BandFresh, false)
	if len(findings) == 0 {
		t.Fatal("a draft using both du and Sie should be caught")
	}
	if findings[0].Rule != "mixed-register" {
		t.Errorf("expected a mixed-register finding, got %q", findings[0].Rule)
	}
}

// Either register held consistently is fine — the check is about mixing, not
// about which one was chosen.
func TestAConsistentRegisterPasses(t *testing.T) {
	for _, body := range []string{
		"Hallo Frank,\n\nich melde mich bei dir, sobald ich deine Notizen " +
			"durchgesehen habe. Sag mir gerne, ob dir das so passt.",
		"Hallo Herr Miller,\n\nich melde mich bei Ihnen, sobald ich Ihre Notizen " +
			"durchgesehen habe. Sagen Sie mir gerne, ob Ihnen das so passt.",
	} {
		if findings := draftcheck.Body(body, textlang.German, convstate.BandFresh, false); len(findings) != 0 {
			t.Errorf("a consistent draft should pass, got %+v for %q", findings, body[:40])
		}
	}
}

// Enumerating the completions of "ich hoffe" failed on a live stack: the list
// held "es geht dir gut" and the model wrote "bei dir ist alles gut". The
// opener is the tell.
func TestEveryIchHoffeOpenerIsCaught(t *testing.T) {
	for _, opener := range []string{
		"Hallo Frank, ich hoffe, bei dir ist alles gut. Vor einigen Wochen...",
		"Hallo Frank, ich hoffe, es geht dir gut. Kurze Rückfrage...",
		"Hallo Herr Miller, ich hoffe, Sie hatten einen guten Start.",
	} {
		if findings := draftcheck.Body(opener, textlang.German, convstate.BandFresh, false); len(findings) == 0 {
			t.Errorf("%q was not caught", opener[:45])
		}
	}
}

// A draft turned the recipient's own CONDITION into a completed fact: the input
// said they would look again once the budget round closed, and the draft wrote
// "as the budget round has now concluded". Nobody said it concluded — and the
// draft then reasoned from it.
//
// It is a claim about THEIR side's state, which is the one thing a drafter
// cannot know: the record holds what they told us, and anything past that is
// invention wearing the grammar of an update.
func TestADraftMayNotDeclareTheirSideResolved(t *testing.T) {
	invented := "Hello Priya, now that the budget round has concluded, I wanted to " +
		"see whether the integration is moving forward."

	findings := draftcheck.Body(invented, textlang.English, convstate.BandMonths, false)
	if len(findings) == 0 {
		t.Fatal("a draft asserting their side resolved something should be caught")
	}
	if findings[0].Rule != "assumed-resolution" {
		t.Errorf("expected an assumed-resolution finding, got %q", findings[0].Rule)
	}
}

// In a LIVE exchange the same words usually refer to something the exchange
// itself established, so the check is gated on the long gap.
func TestTheResolutionCheckIsOnlyForALongSilence(t *testing.T) {
	same := "Hi Priya, now that the review has concluded, here is the scope."

	if f := draftcheck.Body(same, textlang.English, convstate.BandFresh, false); len(f) != 0 {
		t.Errorf("a live exchange may refer to what it established, got %+v", f)
	}
	if f := draftcheck.Body(same, textlang.English, convstate.BandMonths, false); len(f) == 0 {
		t.Error("after months of silence the same sentence is an assertion about them")
	}
}

// The phrasings the model actually produced on the stale-thread fixture, which
// the first list missed.
func TestTheStaleThreadPhrasingsAreCaught(t *testing.T) {
	for _, body := range []string{
		"Hello Priya, when we last spoke regarding the integration project, you mentioned...",
		"Hello Priya, I am following up on our discussion regarding the integration timeline.",
		"Hello Priya, picking up where we left off on the integration.",
	} {
		if f := draftcheck.Body(body, textlang.English, convstate.BandMonths, false); len(f) == 0 {
			t.Errorf("not caught: %q", body[:55])
		}
	}
}

// A subject fails differently from a body: it is one line, it is read before
// anything else, and its worst failure is a CLAIM rather than a phrase.
func TestASubjectMayNotClaimAThreadThatDoesNotExist(t *testing.T) {
	for _, subject := range []string{"Re: Angebot", "AW: Angebot", "Fwd: Angebot", "WG: Angebot"} {
		if f := draftcheck.Subject(subject, textlang.German, convstate.BandFresh, false); len(f) == 0 {
			t.Errorf("%q claims an inbound thread that was never received", subject)
		}
		if f := draftcheck.Subject(subject, textlang.German, convstate.BandFresh, true); len(f) != 0 {
			t.Errorf("%q is correct when a real thread stands behind it, got %+v", subject, f)
		}
	}
}

// At band none there is nothing to follow up ON, so a subject saying so is a
// claim about a message that does not exist.
func TestAFirstTouchSubjectMayNotReferBack(t *testing.T) {
	for lang, subject := range map[textlang.Lang]string{
		textlang.English: "Follow-up on our conversation",
		textlang.German:  "Nachfassen zum Angebot",
	} {
		if f := draftcheck.Subject(subject, lang, convstate.BandNone, false); len(f) == 0 {
			t.Errorf("%s: %q refers back on a first message", lang, subject)
		}
		// The same words are honest once there IS something behind them.
		if f := draftcheck.Subject(subject, lang, convstate.BandWeeks, false); len(f) != 0 {
			t.Errorf("%s: %q is fine when a history exists, got %+v", lang, subject, f)
		}
	}
}

// A subject the client truncates loses the part that carried the meaning.
func TestAnOverlongSubjectIsCaught(t *testing.T) {
	long := "Unser Angebot zur Schnittstelle, zum Zeitplan, zur Abnahme und zu den " +
		"weiteren Schritten im Projekt"

	if f := draftcheck.Subject(long, textlang.German, convstate.BandFresh, false); len(f) == 0 {
		t.Errorf("a %d-rune subject should be caught at %d", len([]rune(long)), draftcheck.SubjectMaxRunes)
	}
	if f := draftcheck.Subject("Angebot Schnittstelle", textlang.German, convstate.BandFresh, false); len(f) != 0 {
		t.Errorf("a short subject should pass, got %+v", f)
	}
}

// An empty subject arrives looking like spam.
func TestAnEmptySubjectIsCaught(t *testing.T) {
	if f := draftcheck.Subject("   ", textlang.German, convstate.BandFresh, false); len(f) != 1 {
		t.Errorf("an empty subject should be caught exactly once, got %+v", f)
	}
}
