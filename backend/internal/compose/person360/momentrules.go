// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// The lower rungs of the moment ladder and the machinery every rung shares.
//
// They live beside moments.go rather than inside it because the ladder's ORDER
// is the decision worth reading in one screen, and the individual conditions
// are detail. A reader asking "what does this page open on" should not have to
// scroll through seven rule bodies to find out.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// roleChangeMoment: a seat on a deal changed, or the relationship crossed a
// threshold. Derived from what the page already read, never from a fresh query.
func roleChangeMoment(_ time.Time, page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	change, ok := findChange(page, relstrength.ChangeRepliedAfterGap)
	if !ok {
		return crmcontracts.PersonMoment{}, false
	}
	evidence := []crmcontracts.PersonMomentEvidence{{
		Type:       crmcontracts.PersonMomentEvidenceTypeRelationshipChange,
		Label:      "The relationship changed",
		ObservedAt: &change.At,
	}}
	return crmcontracts.PersonMoment{
		ClaimKey:            "moment:role_change",
		Rule:                crmcontracts.PersonMomentRuleRoleChange,
		RuleVersion:         ptr(ruleVersion),
		EvidenceFingerprint: fingerprintOf(evidence),
		Headline:            "Their role on this deal changed",
		WhyNow:              "Who decides has moved. What worked with the old seat may not work with the new one.",
		Confidence:          crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence:            evidence,
		FreshnessAt:         &change.At,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindOpenRecord,
			Label: "Open the deal",
			State: crmcontracts.PersonMomentActionStateAvailable,
		},
	}, true
}

// missingNextStepMoment: there is an open deal and nothing scheduled with the
// person who sits on it. The gap is the finding.
func missingNextStepMoment(_ time.Time, page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	if page.Commercial == nil || page.Commercial.Deal == nil {
		return crmcontracts.PersonMoment{}, false
	}
	if page.NextMeeting != nil {
		return crmcontracts.PersonMoment{}, false
	}
	if page.NextSteps != nil && len(page.NextSteps.Data) > 0 {
		return crmcontracts.PersonMoment{}, false
	}
	deal := *page.Commercial.Deal
	evidence := []crmcontracts.PersonMomentEvidence{{
		Type:  crmcontracts.PersonMomentEvidenceTypeRelationshipChange,
		Label: fmt.Sprintf("%s has no next step with them", deal.Title),
	}}
	return crmcontracts.PersonMoment{
		ClaimKey:            "moment:missing_next_step",
		Rule:                crmcontracts.PersonMomentRuleMissingNextStep,
		RuleVersion:         ptr(ruleVersion),
		EvidenceFingerprint: fingerprintOf(evidence),
		Headline:            "No next step with them on an open deal",
		WhyNow:              "The deal is live and nothing is scheduled with the person whose seat decides it.",
		Confidence:          crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence:            evidence,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindScheduleMeeting,
			Label: "Book a meeting",
			State: crmcontracts.PersonMomentActionStateAvailable,
			Destination: &crmcontracts.PersonMomentDestination{
				Surface: crmcontracts.PersonMomentDestinationSurfaceRecord,
			},
		},
	}, true
}

// thinRelationshipMoment: nothing has been captured and nobody here knows them.
//
// It is second to last because it is the least urgent thing that can be true,
// and because saying it too eagerly on a record whose activity section was
// simply withheld would be a lie. Both inputs must be READ and empty, not
// absent.
func thinRelationshipMoment(_ time.Time, page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	if page.Activities == nil || page.Network == nil {
		// A section the caller may not read contributes no moment. Absent is
		// not the same as empty, and only one of them is a fact about the
		// relationship.
		return crmcontracts.PersonMoment{}, false
	}
	if len(page.Activities.Data) > 0 || len(page.Network.Colleagues) > 0 {
		return crmcontracts.PersonMoment{}, false
	}
	evidence := []crmcontracts.PersonMomentEvidence{{
		Type:  crmcontracts.PersonMomentEvidenceTypeRelationshipChange,
		Label: "Nothing captured, nobody connected",
	}}
	return crmcontracts.PersonMoment{
		ClaimKey:            "moment:thin_relationship",
		Rule:                crmcontracts.PersonMomentRuleThinRelationship,
		RuleVersion:         ptr(ruleVersion),
		EvidenceFingerprint: fingerprintOf(evidence),
		Headline:            "Nothing is captured about them yet",
		WhyNow:              "There is no correspondence and no colleague who knows them. Everything about this record is still to be learned.",
		Confidence:          crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence:            evidence,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindLogActivity,
			Label: "Log an interaction",
			State: crmcontracts.PersonMomentActionStateAvailable,
		},
	}, true
}

// oldestOverdueCommitment finds the promise of OURS that has been late longest.
//
// Oldest rather than newest: the one that has been waiting longest is the one
// with the most damage already done, and the one a reader would be most
// embarrassed to discover from the other side.
func oldestOverdueCommitment(now time.Time, page *crmcontracts.Person360) (crmcontracts.ConversationClaim, bool) {
	if page.Claims == nil {
		return crmcontracts.ConversationClaim{}, false
	}
	var oldest *crmcontracts.ConversationClaim
	for i := range *page.Claims {
		claim := &(*page.Claims)[i]
		if claim.Kind != crmcontracts.CommitmentOurs || claim.Status != crmcontracts.ConversationClaimStatusOpen {
			continue
		}
		if claim.DueAt == nil || !claim.DueAt.Before(now) {
			continue
		}
		if oldest == nil || claim.DueAt.Before(*oldest.DueAt) {
			oldest = claim
		}
	}
	if oldest == nil {
		return crmcontracts.ConversationClaim{}, false
	}
	return *oldest, true
}

// inboundEvidence names the actual message where the page is showing it, and
// falls back to the bare fact when the timeline is capped past it.
//
// The fallback is honest rather than silent: the claim is true either way, and
// pretending there is a row to open when the reader would land on nothing is
// worse than saying the message is older than this page shows.
func inboundEvidence(page *crmcontracts.Person360, inbound time.Time) crmcontracts.PersonMomentEvidence {
	return directionEvidence(page, inbound, "Their last message")
}

// outboundEvidence is the same lookup for the message WE sent.
func outboundEvidence(page *crmcontracts.Person360, outbound time.Time) []crmcontracts.PersonMomentEvidence {
	return []crmcontracts.PersonMomentEvidence{
		directionEvidence(page, outbound, "Your last message"),
	}
}

func directionEvidence(page *crmcontracts.Person360, at time.Time, fallback string) crmcontracts.PersonMomentEvidence {
	evidence := crmcontracts.PersonMomentEvidence{
		Type:       crmcontracts.PersonMomentEvidenceTypeActivity,
		Label:      fallback,
		ObservedAt: &at,
	}
	if activity, ok := findActivityAt(page, at); ok {
		id := activity.Id
		evidence.Id = &id
		if activity.Subject != nil && *activity.Subject != "" {
			evidence.Label = *activity.Subject
		}
	}
	return evidence
}

// findChange looks up one derived relationship change on the page. It answers
// false when the section was omitted for want of a grant, which is what keeps
// a moment from disclosing something the page itself is withholding.
func findChange(page *crmcontracts.Person360, kind string) (crmcontracts.PersonRelationshipChange, bool) {
	if page.RelationshipChanges == nil {
		return crmcontracts.PersonRelationshipChange{}, false
	}
	for _, c := range *page.RelationshipChanges {
		if string(c.Kind) == kind {
			return c, true
		}
	}
	return crmcontracts.PersonRelationshipChange{}, false
}

// findActivityAt finds the timeline row for an instant the page reported
// separately. The two come from the same transaction, so a match is exact
// rather than approximate.
func findActivityAt(page *crmcontracts.Person360, at time.Time) (crmcontracts.Activity, bool) {
	if page.Activities == nil {
		return crmcontracts.Activity{}, false
	}
	for _, a := range page.Activities.Data {
		if a.OccurredAt.Equal(at) {
			return a, true
		}
	}
	return crmcontracts.Activity{}, false
}

// fingerprintOf digests what a moment fired on, so a dismissal can be held
// against the evidence rather than against the moment's name.
//
// It hashes the identity and timing of each piece — not the label, which is
// prose this build may reword without the underlying fact having moved. A
// reworded headline must not silently un-dismiss a moment the reader put away.
func fingerprintOf(evidence []crmcontracts.PersonMomentEvidence) string {
	// Built as one string and hashed once. sha256's Write never returns an
	// error, but writing through it would still spread unchecked returns
	// across four calls to say something a single Sum says here.
	var b strings.Builder
	for _, e := range evidence {
		b.WriteString(string(e.Type))
		b.WriteByte(0)
		if e.Id != nil {
			b.WriteString(e.Id.String())
		}
		b.WriteByte(0)
		if e.ObservedAt != nil {
			b.WriteString(strconv.FormatInt(e.ObservedAt.UTC().UnixNano(), 10))
		}
		b.WriteByte(0)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// entityType lifts a destination's entity type, which the contract models as a
// nullable enum and therefore a pointer.
func entityType(v crmcontracts.PersonMomentDestinationEntityType) *crmcontracts.PersonMomentDestinationEntityType {
	return &v
}

// prefill lifts the string map the contract carries as an optional object.
func prefill(v map[string]string) *map[string]string { return &v }
