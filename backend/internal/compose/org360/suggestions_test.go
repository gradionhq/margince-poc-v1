// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The suggestion rules are pure functions of an already-assembled 360, so
// they are provable without a database: each test states the situation the
// rule claims to recognize and the situation next door that it must not.

import (
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var suggestNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// newTestID mints one wire id. Every fixture record gets its own, so an
// assertion that a suggestion cited the right record cannot pass by accident.
func newTestID() openapi_types.UUID {
	return openapi_types.UUID(ids.NewV7())
}

func testOrgID(t *testing.T) ids.OrganizationID {
	t.Helper()
	return ids.From[ids.OrganizationKind](ids.NewV7())
}

// message builds one timeline entry of a two-way kind.
func message(kind crmcontracts.ActivityKind, dir crmcontracts.ActivityDirection, at time.Time) crmcontracts.Activity {
	return crmcontracts.Activity{
		Id:         newTestID(),
		Kind:       kind,
		Direction:  &dir,
		OccurredAt: at,
	}
}

func timeline(entries ...crmcontracts.Activity) *crmcontracts.Organization360 {
	return &crmcontracts.Organization360{
		Activities: &crmcontracts.ActivityListResponse{Data: entries},
	}
}

// TestStaleThreadFiresOnOurUnansweredMessage is the rule's whole point: we
// spoke last, long enough ago that a reply was due.
//
// Every two-way channel is covered, because a rep who sent the last message on
// WhatsApp is waiting exactly as long as one who sent an email, and a rule that
// only recognized mail would go quiet on the accounts that talk any other way.
func TestStaleThreadFiresOnOurUnansweredMessage(t *testing.T) {
	for _, kind := range []crmcontracts.ActivityKind{
		crmcontracts.ActivityKindEmail,
		crmcontracts.ActivityKindWhatsapp,
		crmcontracts.ActivityKindTelegram,
	} {
		t.Run(string(kind), func(t *testing.T) {
			orgID := testOrgID(t)
			sent := suggestNow.AddDate(0, 0, -10)
			view := timeline(message(kind, crmcontracts.ActivityDirectionOutbound, sent))

			got := staleThreadSuggestion(orgID, suggestNow, view)
			if got == nil {
				t.Fatalf("no suggestion for a 10-day-old unanswered outbound %s", kind)
			}
			if string(got.Kind) != suggestNoReply {
				t.Errorf("kind = %q, want %q", got.Kind, suggestNoReply)
			}
			if got.Reason == "" {
				t.Error("reason is empty — a suggestion the rep cannot check is a verdict")
			}
			if len(got.Evidence) != 1 || got.Evidence[0].EntityId != view.Activities.Data[0].Id {
				t.Errorf("evidence = %+v, want the message it fired on", got.Evidence)
			}
		})
	}
}

// TestStaleThreadStaysSilentWhenTheyWroteLast guards the direction half of
// the rule. An unanswered INBOUND message is a thread waiting on us — the
// opposite problem, with the opposite action — so telling the rep to chase
// the person who is waiting for their reply would be worse than silence.
func TestStaleThreadStaysSilentWhenTheyWroteLast(t *testing.T) {
	orgID := testOrgID(t)
	view := timeline(
		message(crmcontracts.ActivityKindEmail, crmcontracts.ActivityDirectionInbound, suggestNow.AddDate(0, 0, -10)),
		message(crmcontracts.ActivityKindEmail, crmcontracts.ActivityDirectionOutbound, suggestNow.AddDate(0, 0, -20)),
	)
	if got := staleThreadSuggestion(orgID, suggestNow, view); got != nil {
		t.Fatalf("suggestion %+v for a thread waiting on US", got)
	}
}

// TestStaleThreadStaysSilentInsideTheReplyWindow proves the wait is a real
// threshold, not a formality: a normal reply time must not fire it.
func TestStaleThreadStaysSilentInsideTheReplyWindow(t *testing.T) {
	orgID := testOrgID(t)
	view := timeline(message(
		crmcontracts.ActivityKindEmail, crmcontracts.ActivityDirectionOutbound, suggestNow.AddDate(0, 0, -2)))
	if got := staleThreadSuggestion(orgID, suggestNow, view); got != nil {
		t.Fatalf("suggestion %+v for a message sent 2 days ago", got)
	}
}

// TestStaleThreadIgnoresOurOwnNotes proves a note or a task cannot start a
// wait: nobody owes a reply to something we wrote to ourselves. The email
// UNDER the note is what the rule must read.
func TestStaleThreadIgnoresOurOwnNotes(t *testing.T) {
	orgID := testOrgID(t)
	note := crmcontracts.Activity{
		Id:         newTestID(),
		Kind:       crmcontracts.ActivityKindNote,
		OccurredAt: suggestNow.AddDate(0, 0, -1),
	}
	email := message(crmcontracts.ActivityKindEmail, crmcontracts.ActivityDirectionOutbound, suggestNow.AddDate(0, 0, -30))
	view := timeline(note, email)

	got := staleThreadSuggestion(orgID, suggestNow, view)
	if got == nil {
		t.Fatal("a note written yesterday suppressed a 30-day-old unanswered email")
	}
	if got.Evidence[0].EntityId != email.Id {
		t.Errorf("evidence cites %v, want the email %v", got.Evidence[0].EntityId, email.Id)
	}
}

// TestStaleThreadStaysSilentWithoutADirection proves an unknown direction is
// not read as outbound. A capture that never recorded who spoke cannot
// support advice about who owes a reply.
func TestStaleThreadStaysSilentWithoutADirection(t *testing.T) {
	orgID := testOrgID(t)
	view := timeline(crmcontracts.Activity{
		Id:         newTestID(),
		Kind:       crmcontracts.ActivityKindEmail,
		OccurredAt: suggestNow.AddDate(0, 0, -30),
	})
	if got := staleThreadSuggestion(orgID, suggestNow, view); got != nil {
		t.Fatalf("suggestion %+v for a message with no recorded direction", got)
	}
}

// TestStaleThreadStaysSilentWithoutTheTimeline proves the withheld case: a
// caller with no activity grant gets no advice derived from activities,
// rather than advice derived from their absence.
func TestStaleThreadStaysSilentWithoutTheTimeline(t *testing.T) {
	orgID := testOrgID(t)
	if got := staleThreadSuggestion(orgID, suggestNow, &crmcontracts.Organization360{}); got != nil {
		t.Fatalf("suggestion %+v with the activities section withheld", got)
	}
}

func openDeal(name string, stalled bool) crmcontracts.Organization360Deal {
	return crmcontracts.Organization360Deal{
		DealId:  newTestID(),
		Name:    name,
		Stalled: stalled,
		Status:  crmcontracts.Organization360DealStatusOpen,
	}
}

// TestStalledDealsRaiseOnePerDeal proves each stalled deal is its own
// suggestion with its own subject, so dismissing one does not silence the
// other, and a healthy deal alongside them raises nothing.
func TestStalledDealsRaiseOnePerDeal(t *testing.T) {
	stalledA, stalledB, healthy := openDeal("Renewal", true), openDeal("Expansion", true), openDeal("Pilot", false)
	view := &crmcontracts.Organization360{Deals: &crmcontracts.Organization360Deals{
		Data: []crmcontracts.Organization360Deal{stalledA, healthy, stalledB},
	}}

	got := stalledDealSuggestions(view)
	if len(got) != 2 {
		t.Fatalf("got %d suggestions, want one per stalled deal", len(got))
	}
	if got[0].Fingerprint == got[1].Fingerprint {
		t.Error("both stalled deals share a fingerprint — dismissing one would silence the other")
	}
	for _, suggestion := range got {
		if suggestion.SubjectId == nil || *suggestion.SubjectId == healthy.DealId {
			t.Errorf("subject = %v, want one of the stalled deals", suggestion.SubjectId)
		}
	}
}

// TestNoNextStepFiresOnlyOnAnActiveAccount pins the deliberate narrowness of
// the rule. An open deal with no task is a gap worth naming; a dormant
// account with no task is not, and a surface that says so would teach the rep
// to scroll past it.
func TestNoNextStepFiresOnlyOnAnActiveAccount(t *testing.T) {
	orgID := testOrgID(t)
	noSteps := &struct {
		Data []crmcontracts.Organization360NextStep `json:"data"`
		Page crmcontracts.PageInfo                  `json:"page"`
	}{}

	active := &crmcontracts.Organization360{
		NextSteps: noSteps,
		Deals:     &crmcontracts.Organization360Deals{Data: []crmcontracts.Organization360Deal{openDeal("Renewal", false)}},
	}
	if got := noNextStepSuggestion(orgID, active); got == nil {
		t.Error("no suggestion for an open deal with nothing scheduled")
	}

	dormant := &crmcontracts.Organization360{NextSteps: noSteps, Deals: &crmcontracts.Organization360Deals{}}
	if got := noNextStepSuggestion(orgID, dormant); got != nil {
		t.Errorf("suggestion %+v on a dormant account — nothing there to advance", got)
	}
}

// TestNoNextStepStaysSilentWhenSomethingIsScheduled is the honest-absent
// half: a task on the account already answers "what happens next".
func TestNoNextStepStaysSilentWhenSomethingIsScheduled(t *testing.T) {
	orgID := testOrgID(t)
	view := &crmcontracts.Organization360{
		NextSteps: &struct {
			Data []crmcontracts.Organization360NextStep `json:"data"`
			Page crmcontracts.PageInfo                  `json:"page"`
		}{Data: []crmcontracts.Organization360NextStep{{
			ActivityId: newTestID(), Subject: "Call the CFO",
		}}},
		Deals: &crmcontracts.Organization360Deals{Data: []crmcontracts.Organization360Deal{openDeal("Renewal", false)}},
	}
	if got := noNextStepSuggestion(orgID, view); got != nil {
		t.Fatalf("suggestion %+v with a task already on the account", got)
	}
}

// TestNoNextStepRidesTheOpenDeals proves the fingerprint tracks WHICH deals
// are open. A dismissal must not carry over to a different pipeline: closing
// one deal and opening another is a new situation, and the advice re-arms.
func TestNoNextStepRidesTheOpenDeals(t *testing.T) {
	orgID := testOrgID(t)
	noSteps := &struct {
		Data []crmcontracts.Organization360NextStep `json:"data"`
		Page crmcontracts.PageInfo                  `json:"page"`
	}{}
	first := noNextStepSuggestion(orgID, &crmcontracts.Organization360{
		NextSteps: noSteps,
		Deals:     &crmcontracts.Organization360Deals{Data: []crmcontracts.Organization360Deal{openDeal("Renewal", false)}},
	})
	second := noNextStepSuggestion(orgID, &crmcontracts.Organization360{
		NextSteps: noSteps,
		Deals:     &crmcontracts.Organization360Deals{Data: []crmcontracts.Organization360Deal{openDeal("Expansion", false)}},
	})
	if first == nil || second == nil {
		t.Fatal("both accounts should raise the suggestion")
	}
	if first.Fingerprint == second.Fingerprint {
		t.Error("a different set of open deals produced the same fingerprint")
	}
}

// TestFingerprintSeparatesKindAndEvidence proves the two things a durable
// dismissal depends on: the same situation hashes the same across calls, and
// neither a different kind nor different evidence collides with it.
func TestFingerprintSeparatesKindAndEvidence(t *testing.T) {
	subject := ids.NewV7().String()
	cited := []crmcontracts.OrganizationBriefEvidence{{
		EntityType: crmcontracts.OrganizationBriefEvidenceEntityTypeDeal,
		EntityId:   newTestID(),
	}}
	base := fingerprint(suggestStalledDeal, subject, cited)

	if again := fingerprint(suggestStalledDeal, subject, cited); again != base {
		t.Error("the same situation hashed differently — a dismissal would not hold")
	}
	if other := fingerprint(suggestNoNextStep, subject, cited); other == base {
		t.Error("two kinds on the same subject collided")
	}
	moved := []crmcontracts.OrganizationBriefEvidence{{
		EntityType: cited[0].EntityType, EntityId: newTestID(),
	}}
	if other := fingerprint(suggestStalledDeal, subject, moved); other == base {
		t.Error("changed evidence hashed the same — the dismissal would never re-arm")
	}
}
