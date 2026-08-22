// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

import (
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// now is a fixed instant, so every case below reads as a claim about the data
// rather than as arithmetic against the wall clock.
var now = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

func at(daysAgo int) time.Time { return now.AddDate(0, 0, -daysAgo) }

func ahead(hours int) time.Time { return now.Add(time.Duration(hours) * time.Hour) }

// timelineOf builds the timeline section the rules read.
func timelineOf(rows ...crmcontracts.Activity) *struct {
	Data []crmcontracts.Activity `json:"data"`
	Page crmcontracts.PageInfo   `json:"page"`
} {
	return &struct {
		Data []crmcontracts.Activity `json:"data"`
		Page crmcontracts.PageInfo   `json:"page"`
	}{Data: rows}
}

// meeting builds the next-meeting section at a given distance from now.
func meeting(startsAt time.Time) *crmcontracts.Person360NextMeeting {
	return &crmcontracts.Person360NextMeeting{
		ActivityId: openapi_types.UUID{},
		StartsAt:   startsAt,
		Subject:    ptr("Expansion review"),
	}
}

// The page opens on ONE moment. A reader handed five reasons has been handed
// the choosing back, which is the work the ladder exists to do.
func TestTheLadderSelectsExactlyOneMoment(t *testing.T) {
	// Three rungs could fire at once: a meeting is close, they replied after a
	// long gap, and a promise is overdue.
	replied := at(1)
	page := &crmcontracts.Person360{
		NextMeeting:    meeting(ahead(24)),
		LastInboundAt:  &replied,
		LastOutboundAt: ptr(at(40)),
		Claims: &[]crmcontracts.ConversationClaim{{
			Kind:             crmcontracts.CommitmentOurs,
			Status:           crmcontracts.ConversationClaimStatusOpen,
			Body:             "Send the revised ROI model",
			DueAt:            ptr(at(5)),
			SourceActivityId: openapi_types.UUID{},
			SourceQuote:      "I'll get you the model by Friday",
		}},
	}
	got := deriveMoment(now, page)
	if got.Rule != crmcontracts.PersonMomentRuleMeetingPrep {
		t.Fatalf("meeting prep outranks every rung below it, got %q", got.Rule)
	}
}

// A meeting outside the horizon is a diary entry, not a reason to open the
// page. The rung below it must then win.
func TestAMeetingBeyondSeventyTwoHoursDoesNotWin(t *testing.T) {
	replied := at(1)
	page := &crmcontracts.Person360{
		NextMeeting:    meeting(ahead(96)),
		LastInboundAt:  &replied,
		LastOutboundAt: ptr(at(40)),
	}
	got := deriveMoment(now, page)
	if got.Rule != crmcontracts.PersonMomentRuleReEngaged {
		t.Fatalf("a meeting four days out should not beat a fresh reply, got %q", got.Rule)
	}
}

// An answered conversation owes nothing. The gone-quiet rung reads direction
// rather than "last touch", because last touch cannot tell the two apart.
func TestAnAnsweredConversationIsNotGoneQuiet(t *testing.T) {
	page := &crmcontracts.Person360{
		LastOutboundAt: ptr(at(30)),
		LastInboundAt:  ptr(at(2)),
		Activities:     timelineOf(),
		Network: &struct {
			Colleagues []crmcontracts.PersonNetworkColleague `json:"colleagues"`
		}{},
	}
	got := deriveMoment(now, page)
	if got.Rule == crmcontracts.PersonMomentRuleGoneQuiet {
		t.Fatal("they answered after our last message; silence is not the story")
	}
}

// Our unanswered outbound past the configured rule is the gone-quiet case, and
// the moment must name the rule that produced it — a reader who disagrees with
// a verdict has to be able to see what produced it.
func TestGoneQuietNamesTheRuleItFiredOn(t *testing.T) {
	page := &crmcontracts.Person360{
		LastOutboundAt: ptr(at(9)),
		LastInboundAt:  ptr(at(16)),
	}
	got := deriveMoment(now, page)
	if got.Rule != crmcontracts.PersonMomentRuleGoneQuiet {
		t.Fatalf("nine days unanswered past a seven-day rule is gone quiet, got %q", got.Rule)
	}
	if got.WhyNow == "" || got.RecommendedAction.Destination == nil {
		t.Fatal("the moment must state its rule and name where its action goes")
	}
	if got.RecommendedAction.Destination.Surface != crmcontracts.PersonMomentDestinationSurfaceComposer {
		t.Fatalf("drafting a follow-up opens the composer, got %q", got.RecommendedAction.Destination.Surface)
	}
}

// An action a rep can press has to go somewhere. The "Ask for context" button
// shipped enabled with no destination, so it rendered as a live control and did
// nothing at all — which teaches a reader that the page has dead buttons, a
// worse outcome than the action being visibly unavailable.
//
// The rule is general: any action offered as available names a destination it
// can actually reach, and any action that cannot reach one says so with a
// reason. The frontend disables a blocked action and shows the reason; what was
// missing was the backend saying it.
//
// It walks the LADDER rather than a handful of pages, and that is the point. A
// first version drove deriveMoment with two shapes, reached two of eight rungs,
// and passed while three dead buttons sat on rungs it never ran. A rung that
// only the winning page reaches is a rung no test covers.
func TestEveryOfferedActionEitherGoesSomewhereOrSaysWhyItCannot(t *testing.T) {
	// One page per rung, each built to make that rung fire.
	pages := map[string]*crmcontracts.Person360{
		"meeting prep": {NextMeeting: &crmcontracts.Person360NextMeeting{StartsAt: ahead(24)}},
		"gone quiet":   {LastOutboundAt: ptr(at(9)), LastInboundAt: ptr(at(16))},
		"thin relationship": {
			Activities: timelineOf(),
			Network: &struct {
				Colleagues []crmcontracts.PersonNetworkColleague `json:"colleagues"`
			}{},
		},
		"role change": {
			RelationshipChanges: &[]crmcontracts.PersonRelationshipChange{{
				Kind: crmcontracts.PersonRelationshipChangeKind(relstrength.ChangeRepliedAfterGap),
				At:   at(2),
			}},
			Commercial: &crmcontracts.Person360Commercial{Deal: &crmcontracts.Person360CommercialDeal{
				DealId: openapi_types.UUID(ids.NewV7()), Title: "Dispatch integration",
			}},
		},
		// The same rung with NO deal visible, which is the reader who lacks the
		// deal grant: the action must block rather than point at a record they
		// cannot open.
		"role change, no deal": {
			RelationshipChanges: &[]crmcontracts.PersonRelationshipChange{{
				Kind: crmcontracts.PersonRelationshipChangeKind(relstrength.ChangeRepliedAfterGap),
				At:   at(2),
			}},
		},
		"missing next step": {
			Commercial: &crmcontracts.Person360Commercial{Deal: &crmcontracts.Person360CommercialDeal{
				DealId: openapi_types.UUID(ids.NewV7()), Title: "Dispatch integration",
			}},
		},
		// Rung 2 wants inbound that arrived after a long silence of ours.
		"re-engaged": {
			LastOutboundAt: ptr(at(40)),
			LastInboundAt:  ptr(at(3)),
		},
		// Rung 4 wants an open commitment OF OURS whose date has passed.
		"overdue promise": {
			Claims: &[]crmcontracts.ConversationClaim{{
				Kind:             crmcontracts.CommitmentOurs,
				Status:           crmcontracts.ConversationClaimStatusOpen,
				Body:             "Send the revised dispatch quote",
				SourceQuote:      "Ich schicke dir das Angebot bis Freitag.",
				SourceActivityId: openapi_types.UUID(ids.NewV7()),
				DueAt:            ptr(at(6)),
			}},
		},
		"nothing needed": {},
	}
	for name, page := range pages {
		t.Run(name, func(t *testing.T) {
			assertActionsAreHonest(t, deriveMoment(now, page))
		})
	}

	// And every rung directly, because deriveMoment stops at the first rung that
	// fires: a page reaching rung 1 says nothing about rung 9, and the first
	// version of this test passed while three lower rungs offered dead buttons.
	//
	// Asking each rung is not enough on its own. A rung no page triggers returns
	// ok=false every time and is judged by nothing, which reads as covered and
	// is not — so a rung that never fires fails here rather than passing quietly.
	t.Run("every rung", func(t *testing.T) {
		fired := make(map[int]bool, len(momentLadder))
		for _, page := range pages {
			for i, rung := range momentLadder {
				moment, ok := rung(now, page)
				if !ok {
					continue
				}
				fired[i] = true
				assertActionsAreHonest(t, moment)
			}
		}
		for i, name := range momentLadderNames {
			if !fired[i] {
				t.Errorf("ladder rung %q fires for none of these pages, so its actions are judged by nothing — add a page that reaches it", name)
			}
		}
	})
}

// dispatchedByThePersonPage is the set of destination surfaces the person page
// actually opens — the `switch` in frontend/src/screens/personpage.tsx.
//
// The contract admits more surfaces than the page handles, and the ones it does
// not handle fall to a `default` that deliberately does nothing. So a
// contract-valid destination is not the bar: an action pointing at `task` is
// enabled, pressed, and inert, which is the dead-button defect this test was
// written for. This list is the bar, and it is a hand-kept mirror of that
// switch — a surface added there belongs here, and until it is, offering it
// fails rather than shipping another quiet nothing.
var dispatchedByThePersonPage = map[crmcontracts.PersonMomentDestinationSurface]bool{
	crmcontracts.PersonMomentDestinationSurfaceComposer:     true,
	crmcontracts.PersonMomentDestinationSurfaceResearch:     true,
	crmcontracts.PersonMomentDestinationSurfaceMeetingBrief: true,
	crmcontracts.PersonMomentDestinationSurfaceRecord:       true,
}

// assertActionsAreHonest holds the rule for one moment: available means
// reachable, blocked means explained.
func assertActionsAreHonest(t *testing.T, moment crmcontracts.PersonMoment) {
	t.Helper()
	actions := []crmcontracts.PersonMomentAction{moment.RecommendedAction}
	if moment.SecondaryActions != nil {
		actions = append(actions, *moment.SecondaryActions...)
	}
	for _, action := range actions {
		if action.State == crmcontracts.PersonMomentActionStateBlocked {
			if action.BlockedReason == nil || *action.BlockedReason == "" {
				t.Errorf("%s: %q is blocked and gives no reason, so its tooltip is empty",
					moment.Rule, action.Label)
			}
			continue
		}
		if action.Destination == nil {
			t.Errorf("%s: %q is offered as %q with no destination, so pressing it does nothing",
				moment.Rule, action.Label, action.State)
			continue
		}
		if !dispatchedByThePersonPage[action.Destination.Surface] {
			t.Errorf("%s: %q points at surface %q, which personpage.tsx does not open — pressing it does nothing",
				moment.Rule, action.Label, action.Destination.Surface)
			continue
		}
		// A record surface navigates on the entity id and on nothing else, so
		// one without an id is a destination that goes nowhere — the same dead
		// button wearing a destination.
		if action.Destination.Surface == crmcontracts.PersonMomentDestinationSurfaceRecord &&
			action.Destination.EntityId == nil {
			t.Errorf("%s: %q points at a record with no entity id, so the client cannot navigate",
				moment.Rule, action.Label)
		}
	}
}

// Rung 10 always answers. "Nothing needs you today" is a result the reader came
// for, and an empty card fails to give it.
func TestAQuietRecordStillGetsAnAnswer(t *testing.T) {
	page := &crmcontracts.Person360{
		Activities: timelineOf(crmcontracts.Activity{Id: openapi_types.UUID{}, Kind: "email", OccurredAt: at(3)}),
		Network: &struct {
			Colleagues []crmcontracts.PersonNetworkColleague `json:"colleagues"`
		}{Colleagues: []crmcontracts.PersonNetworkColleague{{}}},
	}
	got := deriveMoment(now, page)
	if got.Rule != crmcontracts.PersonMomentRuleNothingNeeded {
		t.Fatalf("a record with nothing pending gets the quiet success state, got %q", got.Rule)
	}
}

// A section the caller may not read contributes no moment. Absent is not the
// same as empty, and only one of them is a fact about the relationship.
func TestAWithheldSectionDoesNotProduceAThinRelationshipClaim(t *testing.T) {
	page := &crmcontracts.Person360{}
	got := deriveMoment(now, page)
	if got.Rule == crmcontracts.PersonMomentRuleThinRelationship {
		t.Fatal("activities and network were withheld, not empty; the page must not call the relationship thin")
	}
}

// The dismissal is held against the evidence, so it lifts when the evidence
// moves. A fingerprint that ignored the evidence would silence the page about
// the very thing that just changed.
func TestTheFingerprintChangesWhenTheEvidenceMoves(t *testing.T) {
	first := fingerprintOf([]crmcontracts.PersonMomentEvidence{{
		Type: crmcontracts.PersonMomentEvidenceTypeActivity, ObservedAt: ptr(at(9)),
	}})
	later := fingerprintOf([]crmcontracts.PersonMomentEvidence{{
		Type: crmcontracts.PersonMomentEvidenceTypeActivity, ObservedAt: ptr(at(2)),
	}})
	if first == later {
		t.Fatal("a newer message must re-arm a dismissed moment")
	}
}

// Rewording a headline is not the evidence changing. If prose fed the
// fingerprint, every copy edit would silently un-dismiss what readers put away.
func TestTheFingerprintIgnoresProse(t *testing.T) {
	observed := at(9)
	id := openapi_types.UUID{}
	first := fingerprintOf([]crmcontracts.PersonMomentEvidence{{
		Type: crmcontracts.PersonMomentEvidenceTypeActivity, Id: &id, Label: "Their last message", ObservedAt: &observed,
	}})
	reworded := fingerprintOf([]crmcontracts.PersonMomentEvidence{{
		Type: crmcontracts.PersonMomentEvidenceTypeActivity, Id: &id, Label: "The message they sent", ObservedAt: &observed,
	}})
	if first != reworded {
		t.Fatal("the same evidence under a different label is the same evidence")
	}
}

// A section this reader may not see comes back NIL, exactly like a section that
// is genuinely empty. A rule that reads nil as "there is nothing here" then
// states a confident fact about data the page was never allowed to look at:
// "nothing is scheduled" when the schedule was withheld, "nobody is waiting on
// a reply" when the timeline was.
//
// That is the same defect the drafting program exists to remove — asserting
// something the evidence does not carry — arriving through the record page
// instead of through a draft.
func TestARuleDoesNotClaimAbsenceForASectionItCouldNotRead(t *testing.T) {
	deal := &crmcontracts.Person360Commercial{Deal: &crmcontracts.Person360CommercialDeal{
		DealId: openapi_types.UUID(ids.NewV7()), Title: "Dispatch integration",
	}}

	// An open deal with no visible schedule. Allowed to look, the page says
	// nothing is scheduled; forbidden to look, it must not.
	visible := &crmcontracts.Person360{Commercial: deal}
	if got := deriveMoment(now, visible).Rule; got != crmcontracts.PersonMomentRuleMissingNextStep {
		t.Fatalf("with the schedule readable and empty, the gap IS the finding, got %q", got)
	}

	withheldSchedule := &crmcontracts.Person360{
		Commercial: deal,
		SectionsOmitted: []crmcontracts.Person360SectionsOmitted{
			crmcontracts.Person360SectionsOmittedNextMeeting,
		},
	}
	if got := deriveMoment(now, withheldSchedule).Rule; got == crmcontracts.PersonMomentRuleMissingNextStep {
		t.Error("the schedule was withheld, so 'nothing is scheduled' is not something this page knows")
	}
}

// And the quiet fall-through says so too, rather than reporting a withheld
// timeline as a clean bill of health.
func TestNothingNeededAdmitsWhenItCouldNotSeeEverything(t *testing.T) {
	full := deriveMoment(now, &crmcontracts.Person360{})
	if full.Rule != crmcontracts.PersonMomentRuleNothingNeeded {
		t.Fatalf("an empty readable page is the quiet state, got %q", full.Rule)
	}

	partial := deriveMoment(now, &crmcontracts.Person360{
		SectionsOmitted: []crmcontracts.Person360SectionsOmitted{
			crmcontracts.Person360SectionsOmittedActivities,
		},
	})
	if partial.WhyNow == full.WhyNow {
		t.Error("a reader shown only part of the record must not be told nobody is waiting on a reply")
	}
	if !strings.Contains(partial.WhyNow, "not yours to see") {
		t.Errorf("and it must say WHY the picture is partial, got %q", partial.WhyNow)
	}
}
