// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package persondraft_test

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/persondraft"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/draftfloor"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// envelopeFor builds the envelope a resolved correspondence would produce,
// without going through the service - these cases are about what the FLOOR
// writes, given facts already resolved.
func envelopeFor(lang textlang.Lang, band convstate.Band) draftfloor.Envelope {
	return draftfloor.Envelope{
		Language:          string(lang),
		ConversationState: string(band),
		Now:               "2026-08-11T09:00:00Z",
	}
}

// The reported defect, at the floor: a German correspondence produced an
// English draft, because nothing in the drafter knew what language it was
// writing in.
func TestAGermanCorrespondenceGetsAGermanFloorDraft(t *testing.T) {
	draft := persondraft.Deterministic(persondraft.Input{
		Envelope: envelopeFor(textlang.German, convstate.BandFresh),
		Recipient: persondraft.RecipientIn{
			ID: "p1", Name: "Marek Janetzke", FirstName: "Marek", Employer: "Beispiel GmbH",
		},
	})

	if !strings.HasPrefix(draft.Body, "Hallo Marek,") {
		t.Errorf("body opens %q, want a German greeting", firstLine(draft.Body))
	}
	for _, english := range []string{"Would a short call", "Hi Marek", "Hello,"} {
		if strings.Contains(draft.Body, english) {
			t.Errorf("body contains English %q in a German draft:\n%s", english, draft.Body)
		}
	}
}

// The other half of the reported defect: a first message to somebody with no
// history opened by following up on nothing (DRAFT-AC-E-3).
func TestAFirstTouchNeitherFollowsUpNorRepliesToAThread(t *testing.T) {
	for _, lang := range []textlang.Lang{textlang.English, textlang.German, textlang.Vietnamese} {
		draft := persondraft.Deterministic(persondraft.Input{
			Envelope: envelopeFor(lang, convstate.BandNone),
			Recipient: persondraft.RecipientIn{
				ID: "p1", Name: "Marek Janetzke", FirstName: "Marek", Employer: "Beispiel GmbH",
			},
		})

		if strings.HasPrefix(draft.Subject, "Re:") {
			t.Errorf("%s: subject %q replies to a thread that does not exist", lang, draft.Subject)
		}
		for _, invented := range []string{"Following up", "Nachfassen", "Tiếp theo"} {
			if strings.Contains(draft.Subject, invented) {
				t.Errorf("%s: subject %q claims a history with somebody never written to",
					lang, draft.Subject)
			}
		}
	}
}

// A long silence is acknowledged rather than written straight through
// (DRAFT-AC-E-4). The floor cannot know WHAT changed, but it can decline to
// assume nothing did.
func TestALongSilenceIsAcknowledged(t *testing.T) {
	fresh := persondraft.Deterministic(persondraft.Input{
		Envelope:  envelopeFor(textlang.German, convstate.BandFresh),
		Recipient: persondraft.RecipientIn{ID: "p1", FirstName: "Marek"},
	})
	stale := persondraft.Deterministic(persondraft.Input{
		Envelope:  envelopeFor(textlang.German, convstate.BandMonths),
		Recipient: persondraft.RecipientIn{ID: "p1", FirstName: "Marek"},
	})

	if len(strings.TrimSpace(stale.Body)) <= len(strings.TrimSpace(fresh.Body)) {
		t.Errorf("a months-old correspondence should say more than a live one, "+
			"not the same:\nfresh:\n%s\n\nstale:\n%s", fresh.Body, stale.Body)
	}
	if !strings.Contains(stale.Body, "lange zurück") {
		t.Errorf("a months-old German draft should acknowledge the gap:\n%s", stale.Body)
	}
}

// Only a real thread subject earns the reply prefix. A deal name is a topic
// this side chose, and "Re:" on one claims somebody wrote to us about it.
func TestOnlyARealThreadSubjectEarnsTheReplyPrefix(t *testing.T) {
	withDeal := persondraft.Deterministic(persondraft.Input{
		Envelope:  envelopeFor(textlang.English, convstate.BandFresh),
		Recipient: persondraft.RecipientIn{ID: "p1", FirstName: "Marek"},
		Deal:      &persondraft.DealIn{ID: "d1", Name: "Platform rollout"},
	})
	if strings.HasPrefix(withDeal.Subject, "Re:") {
		t.Errorf("a deal name is not a thread: subject %q", withDeal.Subject)
	}

	withThread := persondraft.Deterministic(persondraft.Input{
		Envelope:  envelopeFor(textlang.English, convstate.BandFresh),
		Recipient: persondraft.RecipientIn{ID: "p1", FirstName: "Marek"},
		Recent: []persondraft.ActIn{
			{ID: "a1", Kind: "email", Subject: "Pricing question", At: "2026-08-10T09:00:00Z"},
		},
	})
	if !strings.HasPrefix(withThread.Subject, "Re:") {
		t.Errorf("an inbound thread subject should be replied to: subject %q", withThread.Subject)
	}
}

// The floor says its one thing of substance in the correspondence's language
// too, not only its skeleton. A German draft with an English sentence in the
// middle is not a German draft.
func TestTheSubstanceSentenceIsInTheSameLanguageAsTheSkeleton(t *testing.T) {
	draft := persondraft.Deterministic(persondraft.Input{
		Envelope:  envelopeFor(textlang.German, convstate.BandFresh),
		Recipient: persondraft.RecipientIn{ID: "p1", FirstName: "Marek"},
		Claims: []persondraft.ClaimIn{
			{ID: "c1", Kind: "objection", Body: "den Preis", SourceID: "a1"},
		},
	})

	if !strings.Contains(draft.Body, "noch eine Antwort") {
		t.Errorf("the substance sentence should be German:\n%s", draft.Body)
	}
	if strings.Contains(draft.Body, "I still owe you") {
		t.Errorf("the substance sentence is still English:\n%s", draft.Body)
	}
}

func firstLine(body string) string {
	line, _, _ := strings.Cut(body, "\n")
	return line
}
