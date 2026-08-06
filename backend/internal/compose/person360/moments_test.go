// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

import (
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// now is a fixed instant, so every case below reads as a claim about the data
// rather than as arithmetic against the wall clock.
var now = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

func at(daysAgo int) time.Time { return now.AddDate(0, 0, -daysAgo) }

// kindsOf lists what the rules produced, in order, so a test can assert on
// both what fired and how it ranked.
func kindsOf(moments []crmcontracts.PersonMoment) []string {
	out := make([]string, 0, len(moments))
	for _, m := range moments {
		out = append(out, string(m.Kind))
	}
	return out
}

// activities builds the timeline section the rules read.
func activities(rows ...crmcontracts.Activity) *struct {
	Data []crmcontracts.Activity `json:"data"`
	Page crmcontracts.PageInfo   `json:"page"`
} {
	return &struct {
		Data []crmcontracts.Activity `json:"data"`
		Page crmcontracts.PageInfo   `json:"page"`
	}{Data: rows}
}

// The reader has to be told the most consequential thing first, and the order
// is a fixed editorial judgment rather than a score. Asserting it here is what
// stops a later rule quietly inserting itself above "they came back".
func TestDeriveMomentsRanksTheReasonsInTheOrderARepShouldActOnThem(t *testing.T) {
	replied := at(3)
	quiet := at(60)
	page := &crmcontracts.Person360{
		RelationshipChanges: &[]crmcontracts.PersonRelationshipChange{
			{Kind: crmcontracts.PersonRelationshipChangeKind(relstrength.ChangeWentQuiet), At: quiet, Days: ptr(60)},
			{Kind: crmcontracts.PersonRelationshipChangeKind(relstrength.ChangeRepliedAfterGap), At: replied, Days: ptr(41)},
		},
		LastInboundAt: &replied,
		NextSteps: activities(crmcontracts.Activity{
			Id: openapi_types.UUID{}, Kind: "task", Subject: ptr("Send the pricing"), DueAt: ptr(at(9)),
		}),
	}
	got := kindsOf(deriveMoments(now, page))
	want := []string{"replied_after_gap", "unanswered_inbound", "task_overdue", "went_quiet"}
	if len(got) != len(want) {
		t.Fatalf("moments = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("moment %d = %q, want %q (full order %v)", i, got[i], want[i], got)
		}
	}
}

// Last touch cannot tell the two cases apart: an account we mailed a fortnight
// ago with no reply and one that wrote to us this morning have the same
// last-touch date and opposite meanings. The rule compares directions.
func TestUnansweredInboundFiresOnlyWhenTheirsWasTheLastWord(t *testing.T) {
	theirs := at(5)
	answered := &crmcontracts.Person360{LastInboundAt: &theirs, LastOutboundAt: ptr(at(4))}
	if _, fired := unansweredInboundMoment(now, answered); fired {
		t.Error("a conversation we answered was reported as owing a reply")
	}
	owed := &crmcontracts.Person360{LastInboundAt: &theirs, LastOutboundAt: ptr(at(9))}
	m, fired := unansweredInboundMoment(now, owed)
	if !fired {
		t.Fatal("their message, unanswered for five days, produced no moment")
	}
	if len(m.Evidence) == 0 {
		t.Error("a moment with no evidence is an opinion")
	}
}

// A reply that arrived this morning is not yet a reply anybody has failed to
// answer. Firing immediately would make the card noise on every active thread.
func TestUnansweredInboundWaitsBeforeItCallsSilenceAProblem(t *testing.T) {
	fresh := now.Add(-2 * time.Hour)
	page := &crmcontracts.Person360{LastInboundAt: &fresh}
	if _, fired := unansweredInboundMoment(now, page); fired {
		t.Error("a message from two hours ago was reported as unanswered")
	}
}

// Preparation is worth something before a meeting and nothing after it, and a
// meeting a month out is a diary entry rather than a reason to open the page.
func TestMeetingAheadCoversTheHorizonAndNothingOutsideIt(t *testing.T) {
	page := &crmcontracts.Person360{Activities: activities(
		crmcontracts.Activity{Kind: "meeting", Subject: ptr("Far off"), OccurredAt: now.AddDate(0, 0, 30)},
		crmcontracts.Activity{Kind: "meeting", Subject: ptr("Thursday review"), OccurredAt: now.AddDate(0, 0, 3)},
		crmcontracts.Activity{Kind: "meeting", Subject: ptr("Last month"), OccurredAt: at(30)},
	)}
	m, fired := meetingAheadMoment(now, page)
	if !fired {
		t.Fatal("a meeting three days out produced no moment")
	}
	if m.Evidence[0].Label != "Thursday review" {
		t.Errorf("the moment named %q; the soonest meeting inside the horizon is Thursday review", m.Evidence[0].Label)
	}

	onlyPast := &crmcontracts.Person360{Activities: activities(
		crmcontracts.Activity{Kind: "meeting", Subject: ptr("Last month"), OccurredAt: at(30)},
	)}
	if _, fired := meetingAheadMoment(now, onlyPast); fired {
		t.Error("a meeting that already happened was reported as something to prepare for")
	}
}

// The oldest overdue task is the one that has been waiting longest and the one
// a reader would be most embarrassed to discover.
func TestTaskOverduePicksTheOneThatHasWaitedLongest(t *testing.T) {
	page := &crmcontracts.Person360{NextSteps: activities(
		crmcontracts.Activity{Kind: "task", Subject: ptr("Recent"), DueAt: ptr(at(2))},
		crmcontracts.Activity{Kind: "task", Subject: ptr("Long forgotten"), DueAt: ptr(at(40))},
		crmcontracts.Activity{Kind: "task", Subject: ptr("Not yet due"), DueAt: ptr(now.AddDate(0, 0, 5))},
	)}
	m, fired := taskOverdueMoment(now, page)
	if !fired {
		t.Fatal("two overdue tasks produced no moment")
	}
	if m.Evidence[0].Label != "Long forgotten" {
		t.Errorf("the moment named %q, want the oldest overdue task", m.Evidence[0].Label)
	}
}

// A section the caller may not read contributes no moments. Deriving one from
// an omitted section would disclose through the moment card exactly what the
// page beside it is withholding.
func TestDeriveMomentsSaysNothingAboutSectionsThePageDidNotRead(t *testing.T) {
	if moments := deriveMoments(now, &crmcontracts.Person360{}); len(moments) != 0 {
		t.Errorf("a page with every section omitted produced %d moments: %v", len(moments), kindsOf(moments))
	}
}

// Every moment carries evidence a reader can open, and a claim key stable
// enough that dismissing it survives the evidence moving.
func TestEveryMomentCarriesEvidenceAndAStableClaimKey(t *testing.T) {
	inbound := at(5)
	page := &crmcontracts.Person360{
		RelationshipChanges: &[]crmcontracts.PersonRelationshipChange{
			{Kind: crmcontracts.PersonRelationshipChangeKind(relstrength.ChangeRepliedAfterGap), At: inbound, Days: ptr(41)},
		},
		LastInboundAt: &inbound,
		Activities: activities(crmcontracts.Activity{
			Kind: "email", Subject: ptr("Re: pricing"), OccurredAt: inbound,
		}),
	}
	seen := map[string]bool{}
	for _, m := range deriveMoments(now, page) {
		if m.ClaimKey == "" {
			t.Errorf("%s carries no claim key, so it could never be dismissed", m.Kind)
		}
		if seen[m.ClaimKey] {
			t.Errorf("%s reuses claim key %q; dismissing one would dismiss the other", m.Kind, m.ClaimKey)
		}
		seen[m.ClaimKey] = true
		if len(m.Evidence) == 0 {
			t.Errorf("%s carries no evidence", m.Kind)
		}
		if m.RecommendedAction.Label == "" {
			t.Errorf("%s recommends nothing, so the reader is told a fact and given no move", m.Kind)
		}
	}
}

// The unanswered-inbound moment names the actual message when the timeline is
// showing it, so "open the evidence" lands on the mail rather than nowhere.
func TestUnansweredInboundNamesTheMessageWhenThePageIsShowingIt(t *testing.T) {
	inbound := at(5)
	id := openapi_types.UUID{1, 2, 3}
	page := &crmcontracts.Person360{
		LastInboundAt: &inbound,
		Activities: activities(crmcontracts.Activity{
			Id: id, Kind: "email", Subject: ptr("Re: pricing"), OccurredAt: inbound,
		}),
	}
	m, fired := unansweredInboundMoment(now, page)
	if !fired {
		t.Fatal("no moment")
	}
	if m.Evidence[0].Id == nil || *m.Evidence[0].Id != id {
		t.Error("the moment did not name the message it fired on, so its evidence link goes nowhere")
	}
	if m.Evidence[0].Label != "Re: pricing" {
		t.Errorf("evidence label = %q, want the message's own subject", m.Evidence[0].Label)
	}
}
