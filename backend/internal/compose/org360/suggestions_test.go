// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The suggestion rules over already-read inputs, so each one is provable
// without a database: every test states the situation the rule claims to
// recognize and the situation next door that it must not.
//
// What the rules READ needs a real database and lives in
// compose/integration/org360_suggestions_integration_test.go — including the
// case these cannot state, that the reads look past the section page cap.

import (
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var suggestNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func testOrgID(t *testing.T) ids.OrganizationID {
	t.Helper()
	return ids.From[ids.OrganizationKind](ids.NewV7())
}

// sentAgo is one exchange on the account, that many days back.
func sentAgo(days int, direction crmcontracts.ActivityDirection) lastMessage {
	return lastMessage{
		ID:        ids.NewV7(),
		Direction: string(direction),
		At:        suggestNow.AddDate(0, 0, -days),
	}
}

// TestStaleThreadFiresOnOurUnansweredMessage is the rule's whole point: we
// spoke last, long enough ago that a reply was due.
func TestStaleThreadFiresOnOurUnansweredMessage(t *testing.T) {
	orgID := testOrgID(t)
	newest := sentAgo(10, crmcontracts.ActivityDirectionOutbound)

	got := staleThread(orgID, suggestNow, newest)
	if got == nil {
		t.Fatal("no suggestion for a 10-day-old unanswered outbound message")
	}
	if got.Kind != suggestNoReply {
		t.Errorf("kind = %q, want %q", got.Kind, suggestNoReply)
	}
	if got.Reason == "" {
		t.Error("reason is empty — a suggestion the rep cannot check is a verdict")
	}
	if len(got.Evidence) != 1 || got.Evidence[0].EntityId != openapi_types.UUID(newest.ID) {
		t.Errorf("evidence = %+v, want the message it fired on", got.Evidence)
	}
}

// TestStaleThreadStaysSilentWhenTheyAnsweredLast guards the direction half of
// the rule. An unanswered INBOUND message is a thread waiting on us — the
// opposite problem, with the opposite action — so telling the rep to chase the
// person who is waiting for their reply would be worse than silence.
func TestStaleThreadStaysSilentWhenTheyAnsweredLast(t *testing.T) {
	orgID := testOrgID(t)
	if got := staleThread(orgID, suggestNow, sentAgo(10, crmcontracts.ActivityDirectionInbound)); got != nil {
		t.Fatalf("suggestion %+v for a thread waiting on US", got)
	}
}

// TestStaleThreadStaysSilentInsideTheReplyWindow proves the wait is a real
// threshold, not a formality: a normal reply time must not fire it.
func TestStaleThreadStaysSilentInsideTheReplyWindow(t *testing.T) {
	orgID := testOrgID(t)
	if got := staleThread(orgID, suggestNow, sentAgo(2, crmcontracts.ActivityDirectionOutbound)); got != nil {
		t.Fatalf("suggestion %+v for a message sent 2 days ago", got)
	}
}

// TestStaleThreadStaysSilentWithoutADirection proves an unrecorded direction is
// not read as outbound. A capture that never said who spoke cannot support
// advice about who owes a reply.
func TestStaleThreadStaysSilentWithoutADirection(t *testing.T) {
	orgID := testOrgID(t)
	newest := lastMessage{ID: ids.NewV7(), At: suggestNow.AddDate(0, 0, -30)}
	if got := staleThread(orgID, suggestNow, newest); got != nil {
		t.Fatalf("suggestion %+v for a message with no recorded direction", got)
	}
}

func idle(name string) stalledDeal {
	return stalledDeal{ID: ids.NewV7(), Name: name, IdleSince: suggestNow.AddDate(0, 0, -200)}
}

// liveAccount is a caller holding both grants, on an account with open deals and
// nothing scheduled — the inputs the no-next-step rule fires on.
func liveAccount(openCount int, digest string) suggestionInputs {
	return suggestionInputs{
		timeline: true, pipeline: true,
		open: pipeline{OpenCount: openCount, OpenDigest: digest},
	}
}

// TestStalledDealFingerprintOnlyMovesWhenTheDealIsWorked is the re-arm half of
// the dismissal contract, for the one kind whose subject never changes.
//
// Two properties, and the second is the one that took three attempts. The
// fingerprint must MOVE when the deal is worked and stalls again, or a single
// dismissal silences that deal for good. And it must move only FORWARD — a shape
// the rep has already dismissed must never recur, or that old dismissal comes back
// to life and silences advice they were shown again in between.
func TestStalledDealFingerprintOnlyMovesWhenTheDealIsWorked(t *testing.T) {
	first := idle("Renewal")
	worked := stalledDeal{ID: first.ID, Name: first.Name, IdleSince: suggestNow.AddDate(0, 0, -70)}

	before := stalledDealSuggestions([]stalledDeal{first})
	after := stalledDealSuggestions([]stalledDeal{worked})
	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("got %d and %d suggestions, want one each", len(before), len(after))
	}
	if after[0].Fingerprint == before[0].Fingerprint {
		t.Error("a deal worked and stalled again reuses the earlier fingerprint — " +
			"one dismissal would silence it for good")
	}

	// The same stall keeps its fingerprint, or the dismissal would not hold for as
	// long as the situation does.
	if repeat := stalledDealSuggestions([]stalledDeal{first}); repeat[0].Fingerprint != before[0].Fingerprint {
		t.Error("the same stall hashed differently between reads — a dismissal would not hold")
	}

	// Monotonicity: every episode this deal has ever produced must be distinct, so
	// walking its activity forward can never land back on a dismissed shape.
	seen := map[string]bool{}
	idleAt := first.IdleSince
	for range 5 {
		episode := stalledDeal{ID: first.ID, Name: first.Name, IdleSince: idleAt}.episode()
		if seen[episode] {
			t.Fatalf("episode %q recurred — a dismissal made against it would resurrect", episode)
		}
		seen[episode] = true
		idleAt = idleAt.AddDate(0, 0, 61)
	}
}

// TestStalledDealsRaiseOnePerDeal proves each stalled deal is its own
// suggestion with its own subject, so dismissing one does not silence the other.
func TestStalledDealsRaiseOnePerDeal(t *testing.T) {
	first, second := idle("Renewal"), idle("Expansion")

	got := stalledDealSuggestions([]stalledDeal{first, second})
	if len(got) != 2 {
		t.Fatalf("got %d suggestions, want one per stalled deal", len(got))
	}
	if got[0].Fingerprint == got[1].Fingerprint {
		t.Error("both stalled deals share a fingerprint — dismissing one would silence the other")
	}
	for i, want := range []stalledDeal{first, second} {
		if got[i].SubjectId == nil || *got[i].SubjectId != openapi_types.UUID(want.ID) {
			t.Errorf("suggestion %d names subject %v, want the deal %v it fired on",
				i, got[i].SubjectId, want.ID)
		}
		if !strings.Contains(got[i].Reason, want.Name) {
			t.Errorf("reason %q never names the deal it is about", got[i].Reason)
		}
	}
}

// TestNoNextStepReportsTheAccountsOwnDealCount proves the reason states how
// many deals the ACCOUNT has open, not how many rows this read carried. A count
// bounded by its own read is one a rep cannot tell from a real one.
func TestNoNextStepReportsTheAccountsOwnDealCount(t *testing.T) {
	got := noNextStepSuggestion(testOrgID(t), liveAccount(42, "digest-a"))
	if got == nil {
		t.Fatal("no suggestion for an active account with nothing scheduled")
	}
	if !strings.Contains(got.Reason, "42") {
		t.Errorf("reason %q does not report the 42 open deals", got.Reason)
	}
}

// TestNoNextStepFiresOnlyOnAnActiveAccount pins the deliberate narrowness of
// the rule. An open deal with no task is a gap worth naming; a dormant account
// with no task is not, and a surface that says so would teach the rep to scroll
// past it.
func TestNoNextStepFiresOnlyOnAnActiveAccount(t *testing.T) {
	orgID := testOrgID(t)

	if got := noNextStepSuggestion(orgID, liveAccount(1, "digest-a")); got == nil {
		t.Error("no suggestion for an open deal with nothing scheduled")
	}
	if got := noNextStepSuggestion(orgID, liveAccount(0, "")); got != nil {
		t.Errorf("suggestion %+v on a dormant account — nothing there to advance", got)
	}
}

// TestNoNextStepStaysSilentWhenSomethingIsScheduled is the honest-absent half:
// a task on the account already answers "what happens next".
func TestNoNextStepStaysSilentWhenSomethingIsScheduled(t *testing.T) {
	in := liveAccount(1, "digest-a")
	in.scheduled = true
	if got := noNextStepSuggestion(testOrgID(t), in); got != nil {
		t.Fatalf("suggestion %+v with a task already on the account", got)
	}
}

// TestNoNextStepNeedsBothGrants is the withheld case. A caller who cannot read
// tasks must not be told there are none, so candidateSuggestions runs this rule
// only when the timeline grant is there too — the pipeline alone cannot tell
// "nothing is scheduled" from "you may not see what is".
func TestNoNextStepNeedsBothGrants(t *testing.T) {
	orgID := testOrgID(t)
	dealsOnly := liveAccount(1, "digest-a")
	dealsOnly.timeline = false
	dealsOnly.open.Stalled = []stalledDeal{idle("Renewal")}

	found := candidateSuggestions(orgID, suggestNow, dealsOnly)
	for _, suggestion := range found {
		if suggestion.Kind == suggestNoNextStep {
			t.Error("a no-next-step suggestion reached a caller who cannot read tasks")
		}
	}
	// The advice that grant DOES support still arrives.
	if len(found) == 0 {
		t.Error("a deal reader got no advice at all about a stalled deal they can open")
	}
}

// TestCandidateSuggestionsRunOnlyTheRulesTheGrantsSupport is the other half: a
// timeline reader gets no advice about a pipeline they cannot see.
func TestCandidateSuggestionsRunOnlyTheRulesTheGrantsSupport(t *testing.T) {
	orgID := testOrgID(t)
	timelineOnly := suggestionInputs{
		timeline:  true,
		hasNewest: true,
		newest:    sentAgo(10, crmcontracts.ActivityDirectionOutbound),
		// Present but unreadable: a pipeline the caller has no grant for must not
		// reach the rules even if the struct carries one.
		open: pipeline{OpenCount: 3, Stalled: []stalledDeal{idle("Renewal")}},
	}
	found := candidateSuggestions(orgID, suggestNow, timelineOnly)
	if len(found) != 1 || found[0].Kind != suggestNoReply {
		t.Fatalf("got %+v, want only the no-reply advice a timeline reader can act on", found)
	}
}

// TestNoNextStepRidesEveryOpenDeal proves the fingerprint tracks WHICH deals
// are open, through the digest the read takes over all of them. A dismissal must
// not carry over to a different pipeline: closing one deal and opening another
// is a new situation, and the advice re-arms.
//
// The digest, not a fetched list, is what makes that true for a deal no card
// listed — the case a fingerprint built from a page would miss.
func TestNoNextStepRidesEveryOpenDeal(t *testing.T) {
	orgID := testOrgID(t)
	first := noNextStepSuggestion(orgID, liveAccount(1, "digest-a"))
	second := noNextStepSuggestion(orgID, liveAccount(1, "digest-b"))
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
		EntityId:   openapi_types.UUID(ids.NewV7()),
	}}
	base := fingerprint(string(suggestStalledDeal), subject, cited)

	if again := fingerprint(string(suggestStalledDeal), subject, cited); again != base {
		t.Error("the same situation hashed differently — a dismissal would not hold")
	}
	if other := fingerprint(string(suggestNoNextStep), subject, cited); other == base {
		t.Error("two kinds on the same subject collided")
	}
	moved := []crmcontracts.OrganizationBriefEvidence{{
		EntityType: cited[0].EntityType, EntityId: openapi_types.UUID(ids.NewV7()),
	}}
	if other := fingerprint(string(suggestStalledDeal), subject, moved); other == base {
		t.Error("changed evidence hashed the same — the dismissal would never re-arm")
	}
}

// TestFingerprintShapeIsWhatTheDismissalAccepts ties the two halves together:
// the endpoint validates a shape rather than re-deriving the value, so the shape
// the rules produce has to be the one it accepts — otherwise every dismissal
// would be refused with a 422 nobody could act on.
func TestFingerprintShapeIsWhatTheDismissalAccepts(t *testing.T) {
	produced := fingerprint(string(suggestNoReply), testOrgID(t).String(), nil)
	if !isFingerprint(produced) {
		t.Errorf("the rules produce %q, which the dismissal endpoint would refuse", produced)
	}
	for _, refused := range []string{"", "   ", "not-a-digest", produced + "0", "ABCDEF"} {
		if isFingerprint(refused) {
			t.Errorf("%q passes as a fingerprint", refused)
		}
	}
}
