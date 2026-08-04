// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// Why this contact is worth attention NOW.
//
// The page's opening line is a reason, not a record. "Warm, 73" describes; "she
// replied after 41 quiet days and nobody has answered" is something to do.
//
// Every moment here is DETERMINISTIC — derived by a rule from captured
// activity, never asserted by a model. Three things follow from that, and all
// three are the point:
//
//	Every moment carries evidence the reader can open. A rule knows what it
//	fired on; a paraphrase does not.
//
//	The page opens on a reason at FIRST PAINT. Nothing here waits on a model,
//	so there is no placeholder state pretending to be an answer.
//
//	A moment cannot be wrong in the way a generated one can. It can be
//	unwelcome — which is what dismissal is for — but it cannot assert
//	something that did not happen.
//
// They are derived from the sections this page has ALREADY read rather than
// from fresh queries. That costs nothing extra and buys an invariant: a moment
// can never cite evidence the page beside it is not showing, and a section the
// caller may not read contributes no moments rather than leaking through one.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// momentsServed bounds what reaches the page. Five is a reading limit, not a
// storage one: past five reasons the reader is scanning a list rather than
// being told what to do, which is the failure this card exists to avoid.
const momentsServed = 5

// unansweredAfterDays is how long an inbound message may sit unanswered before
// silence becomes the point. Short, because owing somebody a reply is the one
// thing on this page that is entirely within the reader's control.
const unansweredAfterDays = 2

// meetingHorizonDays is how far ahead a meeting is worth preparing for. A
// month out is a diary entry; this week is a reason to open the page.
const meetingHorizonDays = 7

// momentsSection derives the reasons and drops the ones a human has already
// dismissed.
//
// It runs LAST among the sections so it can read what the others gathered.
func (s *Service) momentsSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, now time.Time, out *crmcontracts.Person360) error {
	candidates := deriveMoments(now, out)
	if len(candidates) == 0 {
		empty := []crmcontracts.PersonMoment{}
		out.Moments = &empty
		return nil
	}
	// The ledger is consulted for the WHOLE record in one read, then filtered
	// in memory: a page derives several moments about the same contact, and
	// asking per moment would be a query per rendered card.
	verdicts, err := s.feedback.VerdictsForTx(ctx, tx, "person", personID.UUID)
	if err != nil {
		return err
	}
	served := make([]crmcontracts.PersonMoment, 0, momentsServed)
	for _, m := range candidates {
		if v, found := verdicts[ai.VerdictLookupKey(ai.ClaimSignal, ai.ClaimKey(m.ClaimKey))]; found &&
			v.Verdict == ai.VerdictSuppressed {
			// Dismissed, and it stays dismissed. The claim key is the moment's
			// PATH, so this survives the evidence changing underneath it —
			// which is exactly the case a fingerprint of the evidence would
			// get wrong, resurfacing the moment the next time a mail arrived.
			continue
		}
		served = append(served, m)
		if len(served) == momentsServed {
			break
		}
	}
	out.Moments = &served
	return nil
}

// deriveMoments evaluates every rule against the page's own sections and
// returns what fired, most consequential first.
//
// The order is a fixed editorial judgment about what a rep should do next, not
// a score: they came back > we owe them a reply > a meeting is coming > work is
// late > the relationship stopped. A number here would imply a precision the
// rules do not have.
func deriveMoments(now time.Time, page *crmcontracts.Person360) []crmcontracts.PersonMoment {
	var out []crmcontracts.PersonMoment
	if m, ok := repliedAfterGapMoment(page); ok {
		out = append(out, m)
	}
	if m, ok := unansweredInboundMoment(now, page); ok {
		out = append(out, m)
	}
	if m, ok := meetingAheadMoment(now, page); ok {
		out = append(out, m)
	}
	if m, ok := taskOverdueMoment(now, page); ok {
		out = append(out, m)
	}
	if m, ok := wentQuietMoment(page); ok {
		out = append(out, m)
	}
	return out
}

// repliedAfterGapMoment: they came back. The strongest reason captured data
// alone can produce, and the one most likely to be acted on the same hour.
func repliedAfterGapMoment(page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	change, ok := findChange(page, relstrength.ChangeRepliedAfterGap)
	if !ok {
		return crmcontracts.PersonMoment{}, false
	}
	days := 0
	if change.Days != nil {
		days = *change.Days
	}
	return crmcontracts.PersonMoment{
		ClaimKey:   "moment:replied_after_gap",
		Kind:       crmcontracts.PersonMomentKindRepliedAfterGap,
		Headline:   fmt.Sprintf("They replied after %d quiet days", days),
		WhyNow:     "A conversation that had stopped has restarted. The window where a reply is expected is now.",
		Confidence: crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence: []crmcontracts.PersonMomentEvidence{{
			Type:       crmcontracts.PersonMomentEvidenceTypeRelationshipChange,
			Label:      fmt.Sprintf("Their reply ended a %d-day silence", days),
			ObservedAt: &change.At,
		}},
		FreshnessAt: &change.At,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindDraftReply,
			Label: "Draft a reply",
			State: crmcontracts.PersonMomentActionStateWillConfirm,
		},
	}, true
}

// unansweredInboundMoment: they wrote and nobody has written back.
//
// It compares the two directions rather than looking at "last touch", because
// last touch cannot tell the two apart — an account we mailed a fortnight ago
// with no reply and one that wrote to us this morning have the same last-touch
// date and opposite meanings.
func unansweredInboundMoment(now time.Time, page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	if page.LastInboundAt == nil {
		return crmcontracts.PersonMoment{}, false
	}
	inbound := *page.LastInboundAt
	if page.LastOutboundAt != nil && !page.LastOutboundAt.Before(inbound) {
		// We answered after they wrote. Nothing is owed.
		return crmcontracts.PersonMoment{}, false
	}
	waiting := int(now.Sub(inbound).Hours() / 24)
	if waiting < unansweredAfterDays {
		return crmcontracts.PersonMoment{}, false
	}
	moment := crmcontracts.PersonMoment{
		ClaimKey:   "moment:unanswered_inbound",
		Kind:       crmcontracts.PersonMomentKindUnansweredInbound,
		Headline:   fmt.Sprintf("They wrote %d days ago and nobody has answered", waiting),
		WhyNow:     "The last message in this conversation is theirs. Every day this waits is a day they are waiting.",
		Confidence: crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence: []crmcontracts.PersonMomentEvidence{
			inboundEvidence(page, inbound),
		},
		FreshnessAt: &inbound,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindDraftReply,
			Label: "Draft a reply",
			State: crmcontracts.PersonMomentActionStateWillConfirm,
		},
	}
	return moment, true
}

// inboundEvidence names the actual message where the page is showing it, and
// falls back to the bare fact when the timeline is capped past it.
//
// The fallback is honest rather than silent: the claim is true either way, and
// pretending there is a row to open when the reader would land on nothing is
// worse than saying the message is older than this page shows.
func inboundEvidence(page *crmcontracts.Person360, inbound time.Time) crmcontracts.PersonMomentEvidence {
	evidence := crmcontracts.PersonMomentEvidence{
		Type:       crmcontracts.PersonMomentEvidenceTypeActivity,
		Label:      "Their last message",
		ObservedAt: &inbound,
	}
	if activity, ok := findActivityAt(page, inbound); ok {
		id := activity.Id
		evidence.Id = &id
		if activity.Subject != nil && *activity.Subject != "" {
			evidence.Label = *activity.Subject
		}
	}
	return evidence
}

// meetingAheadMoment: a meeting with them is coming and is worth preparing for.
func meetingAheadMoment(now time.Time, page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	if page.Activities == nil {
		return crmcontracts.PersonMoment{}, false
	}
	horizon := now.AddDate(0, 0, meetingHorizonDays)
	// The timeline is ordered newest first, so the LAST future meeting in that
	// order is the soonest one — the one being prepared for.
	var next *crmcontracts.Activity
	for i := range page.Activities.Data {
		a := &page.Activities.Data[i]
		if a.Kind != "meeting" || !a.OccurredAt.After(now) || a.OccurredAt.After(horizon) {
			continue
		}
		next = a
	}
	if next == nil {
		return crmcontracts.PersonMoment{}, false
	}
	label := "Meeting"
	if next.Subject != nil && *next.Subject != "" {
		label = *next.Subject
	}
	id := next.Id
	return crmcontracts.PersonMoment{
		ClaimKey:   "moment:meeting_ahead",
		Kind:       crmcontracts.PersonMomentKindMeetingAhead,
		Headline:   fmt.Sprintf("%s on %s", label, next.OccurredAt.Format("2 Jan")),
		WhyNow:     "Preparation is worth something before the meeting and nothing after it.",
		Confidence: crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence: []crmcontracts.PersonMomentEvidence{{
			Type:       crmcontracts.PersonMomentEvidenceTypeActivity,
			Id:         &id,
			Label:      label,
			ObservedAt: &next.OccurredAt,
		}},
		FreshnessAt: &next.OccurredAt,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindOpenRecord,
			Label: "Open the meeting",
			State: crmcontracts.PersonMomentActionStateAvailable,
		},
	}, true
}

// taskOverdueMoment: work filed against this contact has passed its date.
func taskOverdueMoment(now time.Time, page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	if page.NextSteps == nil {
		return crmcontracts.PersonMoment{}, false
	}
	// The oldest overdue task, because that is the one that has been waiting
	// longest and the one a reader would be most embarrassed to discover.
	var oldest *crmcontracts.Activity
	for i := range page.NextSteps.Data {
		t := &page.NextSteps.Data[i]
		if t.DueAt == nil || !t.DueAt.Before(now) {
			continue
		}
		if oldest == nil || t.DueAt.Before(*oldest.DueAt) {
			oldest = t
		}
	}
	if oldest == nil {
		return crmcontracts.PersonMoment{}, false
	}
	label := "A task"
	if oldest.Subject != nil && *oldest.Subject != "" {
		label = *oldest.Subject
	}
	id := oldest.Id
	overdue := int(now.Sub(*oldest.DueAt).Hours() / 24)
	return crmcontracts.PersonMoment{
		ClaimKey:   "moment:task_overdue",
		Kind:       crmcontracts.PersonMomentKindTaskOverdue,
		Headline:   fmt.Sprintf("%q is %d days overdue", label, overdue),
		WhyNow:     "This was promised for a date that has passed.",
		Confidence: crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence: []crmcontracts.PersonMomentEvidence{{
			Type:       crmcontracts.PersonMomentEvidenceTypeTask,
			Id:         &id,
			Label:      label,
			ObservedAt: oldest.DueAt,
		}},
		FreshnessAt: oldest.DueAt,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindCompleteTask,
			Label: "Complete it",
			State: crmcontracts.PersonMomentActionStateAvailable,
		},
	}, true
}

// wentQuietMoment: an established relationship stopped. Last, because it is
// the least urgent of the five and the most likely to be already known.
func wentQuietMoment(page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	change, ok := findChange(page, relstrength.ChangeWentQuiet)
	if !ok {
		return crmcontracts.PersonMoment{}, false
	}
	days := 0
	if change.Days != nil {
		days = *change.Days
	}
	return crmcontracts.PersonMoment{
		ClaimKey:   "moment:went_quiet",
		Kind:       crmcontracts.PersonMomentKindWentQuiet,
		Headline:   fmt.Sprintf("Nothing for %d days", days),
		WhyNow:     "This was an active relationship. It stopped, and nothing has restarted it.",
		Confidence: crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence: []crmcontracts.PersonMomentEvidence{{
			Type:       crmcontracts.PersonMomentEvidenceTypeRelationshipChange,
			Label:      "The last thing that happened",
			ObservedAt: &change.At,
		}},
		FreshnessAt: &change.At,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindDraftReply,
			Label: "Write to them",
			State: crmcontracts.PersonMomentActionStateWillConfirm,
		},
	}, true
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
