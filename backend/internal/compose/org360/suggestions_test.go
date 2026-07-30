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
	return stalledDeal{ID: ids.NewV7(), Name: name}
}

// livePipeline is one open deal the caller can see, with nothing stalled — the
// aggregate shape openPipeline returns.
func livePipeline(count int, digest string) pipeline {
	return pipeline{OpenCount: count, OpenDigest: digest}
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

// noTasks is the next-steps section present and empty — the caller may read
// tasks, and there are none.
func noTasks() *crmcontracts.Organization360 {
	return &crmcontracts.Organization360{
		NextSteps: &struct {
			Data []crmcontracts.Organization360NextStep `json:"data"`
			Page crmcontracts.PageInfo                  `json:"page"`
		}{},
	}
}

// TestNoNextStepReportsTheAccountsOwnDealCount proves the reason states how
// many deals the ACCOUNT has open, not how many rows this read carried. A count
// bounded by its own read is one a rep cannot tell from a real one.
func TestNoNextStepReportsTheAccountsOwnDealCount(t *testing.T) {
	got := noNextStepSuggestion(testOrgID(t), noTasks(), livePipeline(42, "digest-a"))
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

	if got := noNextStepSuggestion(orgID, noTasks(), livePipeline(1, "digest-a")); got == nil {
		t.Error("no suggestion for an open deal with nothing scheduled")
	}
	if got := noNextStepSuggestion(orgID, noTasks(), pipeline{}); got != nil {
		t.Errorf("suggestion %+v on a dormant account — nothing there to advance", got)
	}
}

// TestNoNextStepStaysSilentWhenSomethingIsScheduled is the honest-absent half:
// a task on the account already answers "what happens next".
func TestNoNextStepStaysSilentWhenSomethingIsScheduled(t *testing.T) {
	orgID := testOrgID(t)
	view := &crmcontracts.Organization360{
		NextSteps: &struct {
			Data []crmcontracts.Organization360NextStep `json:"data"`
			Page crmcontracts.PageInfo                  `json:"page"`
		}{Data: []crmcontracts.Organization360NextStep{{
			ActivityId: openapi_types.UUID(ids.NewV7()), Subject: "Call the CFO",
		}}},
	}
	if got := noNextStepSuggestion(orgID, view, livePipeline(1, "digest-a")); got != nil {
		t.Fatalf("suggestion %+v with a task already on the account", got)
	}
}

// TestNoNextStepStaysSilentWithoutTheTaskSection is the withheld case: a caller
// who cannot read tasks must not be told there are none.
func TestNoNextStepStaysSilentWithoutTheTaskSection(t *testing.T) {
	orgID := testOrgID(t)
	withheld := &crmcontracts.Organization360{}
	if got := noNextStepSuggestion(orgID, withheld, livePipeline(1, "digest-a")); got != nil {
		t.Fatalf("suggestion %+v with the next-steps section withheld", got)
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
	first := noNextStepSuggestion(orgID, noTasks(), livePipeline(1, "digest-a"))
	second := noNextStepSuggestion(orgID, noTasks(), livePipeline(1, "digest-b"))
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
