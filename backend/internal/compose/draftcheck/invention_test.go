// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package draftcheck

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// The draft a rep was actually served from a company page, verbatim.
//
// The account had a live support thread and no call of any kind. Every sentence
// of substance here is invented: no meeting happened, the recipient raised no
// challenges, and no record describes an operational setup or any technical
// constraint. It scored zero findings at all four bands, which is what this
// test exists to keep from happening again.
const servedInventedDraft = "Marine, it was a pleasure connecting earlier this week. " +
	"I have been giving some thought to the challenges you mentioned regarding your " +
	"current operational setup and believe there may be some targeted ways to reduce " +
	"those technical constraints.\n\n" +
	"Would you be open to a brief call to explore how we could identify potential " +
	"efficiency gains for your team?"

// A claim about the world is wrong at every band. The gap that let this through
// was band gating: assumed-memory ran only at weeks and months, invention only
// at band none, and a thread two days old sits at fresh, where neither ran.
func TestTheServedInventedDraftIsRefusedAtEveryBand(t *testing.T) {
	for _, band := range []convstate.Band{
		convstate.BandNone, convstate.BandFresh,
		convstate.BandWeeks, convstate.BandMonths,
	} {
		t.Run(string(band), func(t *testing.T) {
			findings := Body(servedInventedDraft, textlang.English, band, false)
			if len(findings) == 0 {
				t.Fatalf("the served draft passed clean at band %s", band)
			}
			if !hasRule(findings, "invented-conversation") {
				t.Errorf("the invented call was not caught at band %s: %+v", band, findings)
			}
			if !hasRule(findings, "attributed-claim") {
				t.Errorf("the invented attribution was not caught at band %s: %+v", band, findings)
			}
		})
	}
}

// A rep reads the correction, so it has to name the phrase back. A finding that
// says only "something is wrong" leaves the model guessing on the retry.
func TestTheCorrectionNamesTheInventedPhrase(t *testing.T) {
	feedback := Feedback(Body(servedInventedDraft, textlang.English, convstate.BandFresh, false))

	if !strings.Contains(feedback, "pleasure connecting") {
		t.Errorf("the correction did not name the invented meeting: %q", feedback)
	}
	if !strings.Contains(feedback, "you mentioned") {
		t.Errorf("the correction did not name the invented attribution: %q", feedback)
	}
}

// The lists must not swallow the ordinary drafts they sit beside. A message
// that asks for a call, or names a topic without attributing it, is the correct
// output and has to stay clean at the band a live thread sits in.
func TestAnHonestDraftStaysClean(t *testing.T) {
	for name, body := range map[string]string{
		"asks for a call without claiming one happened": "Marine, the pricing question on " +
			"the thread is easiest to answer live. Would a short call this week suit you?",
		"names the topic without attributing it": "Marine, on the question about the " +
			"August deadline: we can hold the current terms until the end of the month.",
		"refers to a message rather than a conversation": "Marine, following the note about " +
			"the provider comparison, here is what our side can commit to.",
	} {
		t.Run(name, func(t *testing.T) {
			if findings := Body(body, textlang.English, convstate.BandFresh, false); len(findings) > 0 {
				t.Fatalf("an honest draft was refused: %+v", findings)
			}
		})
	}
}

// A reply is written FROM the counterparty's own message. If they wrote "as I
// mentioned on our call", answering the call they named is grounded in text the
// drafter can see — so the world-claim rules stand down on a threaded draft and
// hold on one that opens a new conversation.
func TestAThreadedReplyMayAnswerACallTheCounterpartyNamed(t *testing.T) {
	body := "Thanks — after our call I pulled the figures you mentioned, and the " +
		"August timeline still works on our side."

	if findings := Body(body, textlang.English, convstate.BandFresh, true); len(findings) > 0 {
		t.Fatalf("a grounded reply was refused: %+v", findings)
	}
	if findings := Body(body, textlang.English, convstate.BandFresh, false); len(findings) == 0 {
		t.Fatal("the same words opening a new conversation must still be refused")
	}
}

// The lists must assert a COMPLETED exchange on their own. A draft proposing a
// call, or naming one still to come, is the output this product exists to write
// — refusing it would be worse than the defect the lists were added for.
func TestForwardLookingAndUnrelatedTextIsNotAnInventedCall(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		lang textlang.Lang
	}{
		"proposes a call":            {"It would be great to connect next week.", textlang.English},
		"names a call still to come": {"We can cover that in our call tomorrow.", textlang.English},
		"connects two people":        {"Good to connect you with Anna, who runs delivery.", textlang.English},
		"asks for a call":            {"Shall we set up a call?", textlang.English},
		"proposes a German call":     {"Es freut mich, dass wir nächste Woche sprechen können.", textlang.German},
		"a German call to come":      {"Unser Telefonat nächsten Dienstag passt mir gut.", textlang.German},
	} {
		t.Run(name, func(t *testing.T) {
			if findings := Body(tc.body, tc.lang, convstate.BandFresh, false); len(findings) > 0 {
				t.Fatalf("a legitimate draft was refused: %+v", findings)
			}
		})
	}
}

// German drafts fail the same way in their own words, and a list that only
// knows English lets every German draft through — the failure mode the wellbeing
// list already learned once.
func TestGermanInventionIsCaught(t *testing.T) {
	for name, body := range map[string]string{
		"an invented call":        "Marine, es freute mich sehr, letzte Woche mit Ihnen zu sprechen.",
		"an invented attribution": "Marine, Sie erwähnten den Zeitplan für August.",
		"after a phone call":      "Marine, nach unserem Telefonat habe ich die Zahlen geprüft.",
	} {
		t.Run(name, func(t *testing.T) {
			if findings := Body(body, textlang.German, convstate.BandFresh, false); len(findings) == 0 {
				t.Fatalf("a German invention passed clean")
			}
		})
	}
}

func hasRule(findings []Finding, rule string) bool {
	for _, f := range findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}
