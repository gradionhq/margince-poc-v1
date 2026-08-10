// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

import (
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// now is a fixed instant, so every case below reads as a claim about the data
// rather than as arithmetic against the wall clock.
var now = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

func at(daysAgo int) time.Time { return now.AddDate(0, 0, -daysAgo) }

func ahead(hours int) time.Time { return now.Add(time.Duration(hours) * time.Hour) }

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
	got, ok := deriveMoment(now, page)
	if !ok {
		t.Fatal("the ladder returned nothing; rung 10 must always answer")
	}
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
	got, _ := deriveMoment(now, page)
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
		Activities:     activities(),
		Network: &struct {
			Colleagues []crmcontracts.PersonNetworkColleague `json:"colleagues"`
		}{},
	}
	got, _ := deriveMoment(now, page)
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
	got, _ := deriveMoment(now, page)
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

// Rung 10 always answers. "Nothing needs you today" is a result the reader came
// for, and an empty card fails to give it.
func TestAQuietRecordStillGetsAnAnswer(t *testing.T) {
	page := &crmcontracts.Person360{
		Activities: activities(crmcontracts.Activity{Id: openapi_types.UUID{}, Kind: "email", OccurredAt: at(3)}),
		Network: &struct {
			Colleagues []crmcontracts.PersonNetworkColleague `json:"colleagues"`
		}{Colleagues: []crmcontracts.PersonNetworkColleague{{}}},
	}
	got, ok := deriveMoment(now, page)
	if !ok || got.Rule != crmcontracts.PersonMomentRuleNothingNeeded {
		t.Fatalf("a record with nothing pending gets the quiet success state, got %q", got.Rule)
	}
}

// A section the caller may not read contributes no moment. Absent is not the
// same as empty, and only one of them is a fact about the relationship.
func TestAWithheldSectionDoesNotProduceAThinRelationshipClaim(t *testing.T) {
	page := &crmcontracts.Person360{}
	got, _ := deriveMoment(now, page)
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
