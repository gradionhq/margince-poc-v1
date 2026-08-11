// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package persondraft

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/draftfloor"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

func envelope(lang textlang.Lang, band convstate.Band) draftfloor.Envelope {
	return draftfloor.Envelope{
		Language:          string(lang),
		ConversationState: string(band),
		Now:               "2026-08-11T09:00:00Z",
	}
}

// An overdue promise is the reason to write today, so it leads — over a
// question they asked, which is the kind that led before.
//
// This is the archetype the grounding work is for: the email a rep knows they
// should send and does not.
func TestAnOverduePromiseLeadsTheDraft(t *testing.T) {
	draft := Deterministic(Input{
		Envelope:  envelope(textlang.English, convstate.BandWeeks),
		Recipient: RecipientIn{ID: "p1", FirstName: "Priya"},
		Claims: []ClaimIn{
			{ID: "c1", Kind: "open_question", Body: "the API rate limits", SourceID: "a1"},
			{ID: "c2", Kind: "commitment", Body: "the integration scope document",
				SourceID: "a2", Due: "2026-07-25T00:00:00Z", Overdue: true},
		},
	})

	if !strings.Contains(draft.Body, "integration scope document") {
		t.Errorf("the overdue promise should lead:\n%s", draft.Body)
	}
	if strings.Contains(draft.Body, "API rate limits") {
		t.Errorf("a question should not lead while a promise is outstanding:\n%s", draft.Body)
	}
}

// A promise NOT yet due is not a reason to write today. Leading on it invents
// an urgency nobody agreed to.
func TestAPromiseStillWithinItsDateDoesNotLead(t *testing.T) {
	draft := Deterministic(Input{
		Envelope:  envelope(textlang.English, convstate.BandWeeks),
		Recipient: RecipientIn{ID: "p1", FirstName: "Priya"},
		Claims: []ClaimIn{
			{ID: "c1", Kind: "open_question", Body: "the API rate limits", SourceID: "a1"},
			{ID: "c2", Kind: "commitment", Body: "the integration scope document",
				SourceID: "a2", Due: "2026-09-30T00:00:00Z"},
		},
	})

	if !strings.Contains(draft.Body, "API rate limits") {
		t.Errorf("with nothing overdue, the question they asked should lead:\n%s", draft.Body)
	}
}

// A commitment with no date at all never leads. "We said we would look into it"
// is not a promise with a deadline, and treating it as one manufactures a
// reason to write.
func TestAPromiseWithNoDateNeverLeads(t *testing.T) {
	draft := Deterministic(Input{
		Envelope:  envelope(textlang.English, convstate.BandWeeks),
		Recipient: RecipientIn{ID: "p1", FirstName: "Priya"},
		Claims: []ClaimIn{
			{ID: "c1", Kind: "objection", Body: "the price", SourceID: "a1"},
			{ID: "c2", Kind: "commitment", Body: "looking into the integration", SourceID: "a2"},
		},
	})

	if !strings.Contains(draft.Body, "the price") {
		t.Errorf("an undated commitment should not displace a real objection:\n%s", draft.Body)
	}
}

// The negative gate: the DATE is grounding for the writer and must not reach
// the recipient as a raw timestamp. A person writes "last month", not
// "2026-07-25T00:00:00Z".
func TestTheDueDateNeverReachesTheBodyAsATimestamp(t *testing.T) {
	draft := Deterministic(Input{
		Envelope:  envelope(textlang.German, convstate.BandWeeks),
		Recipient: RecipientIn{ID: "p1", FirstName: "Marek"},
		Claims: []ClaimIn{
			{ID: "c1", Kind: "commitment", Body: "das Angebot",
				SourceID: "a1", Due: "2026-07-25T00:00:00Z", Overdue: true},
		},
	})

	for _, leaked := range []string{"2026-07-25", "T00:00:00Z", "RFC3339"} {
		if strings.Contains(draft.Body, leaked) {
			t.Errorf("the body leaked the raw date %q:\n%s", leaked, draft.Body)
		}
	}
}

// The overdue reading is the ENVELOPE's clock, not a fresh one. A draft stamped
// at one instant and judging dates at another can call the same promise overdue
// on one line and pending on the next.
func TestOverdueIsReadFromTheEnvelopesOwnClock(t *testing.T) {
	stamped := envelope(textlang.English, convstate.BandWeeks)
	if got := stamped.At().Format("2006-01-02"); got != "2026-08-11" {
		t.Fatalf("the envelope's clock reads %s, want 2026-08-11", got)
	}

	// An envelope with no readable stamp answers the zero time, and every
	// comparison against it treats a due date as "not yet" rather than
	// declaring everything overdue.
	unstamped := draftfloor.Envelope{}
	if !unstamped.At().IsZero() {
		t.Error("an unstamped envelope should answer the zero time")
	}
}
