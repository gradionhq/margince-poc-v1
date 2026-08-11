// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// Why this contact needs attention TODAY — one answer, not a list.
//
// The page's opening line is a reason, not a record. "Warm, 73" describes; "she
// replied after 41 quiet days and nobody has answered" is something to do.
//
// ONE MOMENT WINS (ADR-0096 D2). A card offering five reasons has handed the
// choosing back to the reader, which is the work this ladder exists to do. The
// ladder is fixed and ordered by consequence; the first rung that fires is the
// answer, and the rest are not computed into a list nobody reads.
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
//
// Two rungs are input-bound and stay dormant rather than being mocked into
// life: job_change fires only on a RECORDED employment change, and
// public_signal needs a connected data provider. A rule that cannot fire is
// absent from the page, not an empty card.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ruleVersion stamps the ladder that selected a moment. It changes whenever a
// rung's condition or order changes, so the same evidence rendering differently
// across two clients is visible rather than silent.
const ruleVersion = "person-moment-ladder-v1"

// meetingHorizonHours is how far ahead a meeting is worth preparing for
// (ADR-0096 D2 rung 1). Three days, not the week the earlier ladder used: a
// meeting on Friday is not what a reader should be told to do about on Monday,
// and the prep moment is worth something only while there is still time to act
// on it.
const meetingHorizonHours = 72

// reEngagedQuietDays is the silence a new inbound has to break before its
// arrival is a moment rather than ordinary correspondence.
const reEngagedQuietDays = 14

// goneQuietAfterDays is how long our outbound may go unanswered before the
// silence is the point. It is the configured rule the moment names in its own
// text, so the reader can see what verdict they are being shown.
const goneQuietAfterDays = 7

// prefillIntent is the prefill key a composer reads to know what it is opening
// for. One spelling, because a typo here is a silently empty drawer.
const prefillIntent = "intent"

// momentsSection selects the one moment this page opens on, and honours a
// dismissal the viewer has already made against the same evidence.
//
// It runs LAST among the sections so it can read what the others gathered.
func (s *Service) momentsSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, now time.Time, out *crmcontracts.Person360) error {
	moment := deriveMoment(now, out)
	dismissed, err := s.momentDismissed(ctx, tx, personID, moment)
	if err != nil {
		return err
	}
	if dismissed {
		// Dismissed against THIS evidence. The quiet success state is what the
		// reader asked for by dismissing, and it is a moment of its own rather
		// than an empty card.
		quiet := nothingNeededMoment(now)
		out.Moment = &quiet
		return nil
	}
	out.Moment = &moment
	return nil
}

// momentDismissed asks whether this viewer has already put this moment away
// AND the evidence has not moved since.
//
// The fingerprint comparison is the whole mechanism. A dismissal keyed on the
// moment's path alone survives the world changing underneath it: the reader
// dismisses "she went quiet", a reply arrives, and the page stays silent about
// the thing that just changed. Keyed on the evidence, the dismissal re-arms.
func (s *Service) momentDismissed(ctx context.Context, tx pgx.Tx, personID ids.PersonID, moment crmcontracts.PersonMoment) (bool, error) {
	// A dismissal belongs to a person's screen, so a call carrying no user has
	// none to honour. An agent reading through a passport must not consume the
	// granting human's: it sees every moment. This is a fact about the caller,
	// not a failure to read.
	viewer, ok := principal.Actor(ctx)
	if !ok || viewer.UserID == (ids.UUID{}) {
		return false, nil
	}
	var stored string
	// The workspace is named rather than left to row-level security alone. RLS
	// does not apply to a superuser or a BYPASSRLS role — which is what every
	// migration and CI run uses — and without the predicate this QueryRow would
	// silently take the first of several rows across workspaces.
	err := tx.QueryRow(ctx, `
		SELECT evidence_fingerprint
		FROM person_moment_dismissal
		WHERE workspace_id = $1 AND user_id = $2 AND person_id = $3 AND claim_key = $4`,
		storekit.MustWorkspace(ctx), viewer.UserID, personID, moment.ClaimKey).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read moment dismissal: %w", err)
	}
	return stored == moment.EvidenceFingerprint, nil
}

// deriveMoment walks the ladder in ADR-0096's fixed order and returns the first
// rung that fires.
//
// The order is the decision, not a score: a meeting in two days outranks a
// silence of three weeks because one has a deadline and the other does not. A
// number here would imply a precision the rules do not have.
//
// Rungs 3 (job change) and 7 (public signal) are absent by design — both need
// inputs this build does not have, and a rule that cannot fire belongs nowhere
// on the page.
func deriveMoment(now time.Time, page *crmcontracts.Person360) crmcontracts.PersonMoment {
	ladder := []func(time.Time, *crmcontracts.Person360) (crmcontracts.PersonMoment, bool){
		meetingPrepMoment,      // 1. a meeting within 72 hours
		reEngagedMoment,        // 2. new inbound after a material quiet period
		overduePromiseMoment,   // 4. an open commitment of ours is overdue
		goneQuietMoment,        // 5. outbound unanswered past the configured rule
		roleChangeMoment,       // 6. a new deal role or material relationship change
		missingNextStepMoment,  // 8. an open deal with no next step involving them
		thinRelationshipMoment, // 9. no captured interaction or network
	}
	for _, rule := range ladder {
		if moment, ok := rule(now, page); ok {
			return moment
		}
	}
	// 10. Nothing needs you today. A quiet success state, not a blank card:
	// "there is nothing here" is an answer, and the reader came for an answer.
	return nothingNeededMoment(now)
}

// meetingPrepMoment: a meeting is close enough that preparing for it is the
// most valuable thing the reader could do.
func meetingPrepMoment(now time.Time, page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	if page.NextMeeting == nil {
		return crmcontracts.PersonMoment{}, false
	}
	meeting := *page.NextMeeting
	if !meeting.StartsAt.After(now) || meeting.StartsAt.After(now.Add(meetingHorizonHours*time.Hour)) {
		return crmcontracts.PersonMoment{}, false
	}
	label := "Meeting"
	if meeting.Subject != nil && *meeting.Subject != "" {
		label = *meeting.Subject
	}
	id := meeting.ActivityId
	evidence := []crmcontracts.PersonMomentEvidence{{
		Type:       crmcontracts.PersonMomentEvidenceTypeActivity,
		Id:         &id,
		Label:      label,
		ObservedAt: &meeting.StartsAt,
	}}
	return crmcontracts.PersonMoment{
		ClaimKey:            "moment:meeting_prep",
		Rule:                crmcontracts.PersonMomentRuleMeetingPrep,
		RuleVersion:         ptr(ruleVersion),
		EvidenceFingerprint: fingerprintOf(evidence),
		Headline:            fmt.Sprintf("Prepare for %s", label),
		WhyNow:              "Preparation is worth something before the meeting and nothing after it.",
		Confidence:          crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence:            evidence,
		FreshnessAt:         &meeting.StartsAt,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindOpenMeetingBrief,
			Label: "Open meeting brief",
			State: crmcontracts.PersonMomentActionStateAvailable,
			Destination: &crmcontracts.PersonMomentDestination{
				Surface:    crmcontracts.PersonMomentDestinationSurfaceMeetingBrief,
				EntityType: entityType(crmcontracts.PersonMomentDestinationEntityTypeActivity),
				EntityId:   &id,
			},
		},
		SecondaryActions: &[]crmcontracts.PersonMomentAction{{
			Kind:  crmcontracts.PersonMomentActionKindDraftReply,
			Label: "Draft agenda",
			State: crmcontracts.PersonMomentActionStateWillConfirm,
			Destination: &crmcontracts.PersonMomentDestination{
				Surface: crmcontracts.PersonMomentDestinationSurfaceComposer,
				Prefill: prefill(map[string]string{prefillIntent: "agenda"}),
			},
		}},
	}, true
}

// reEngagedMoment: they came back. The strongest reason captured data alone can
// produce, and the one most likely to be acted on the same hour.
func reEngagedMoment(now time.Time, page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	if page.LastInboundAt == nil {
		return crmcontracts.PersonMoment{}, false
	}
	inbound := *page.LastInboundAt
	if page.LastOutboundAt != nil && !page.LastOutboundAt.Before(inbound) {
		// We answered after they wrote. Nothing is owed, and their message is
		// not news.
		return crmcontracts.PersonMoment{}, false
	}
	// The silence this message broke: measured against our own last outbound,
	// because a gap only means something relative to what came before it.
	if page.LastOutboundAt == nil {
		return crmcontracts.PersonMoment{}, false
	}
	quiet := int(inbound.Sub(*page.LastOutboundAt).Hours() / 24)
	if quiet < reEngagedQuietDays {
		return crmcontracts.PersonMoment{}, false
	}
	evidence := []crmcontracts.PersonMomentEvidence{inboundEvidence(page, inbound)}
	return crmcontracts.PersonMoment{
		ClaimKey:            "moment:re_engaged",
		Rule:                crmcontracts.PersonMomentRuleReEngaged,
		RuleVersion:         ptr(ruleVersion),
		EvidenceFingerprint: fingerprintOf(evidence),
		Headline:            fmt.Sprintf("They replied after %d quiet days", quiet),
		WhyNow:              "A conversation that had stopped has restarted. The window where a reply is expected is now.",
		Confidence:          crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence:            evidence,
		FreshnessAt:         &inbound,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindDraftReply,
			Label: "Draft a reply",
			State: crmcontracts.PersonMomentActionStateWillConfirm,
			Destination: &crmcontracts.PersonMomentDestination{
				Surface: crmcontracts.PersonMomentDestinationSurfaceComposer,
				Prefill: prefill(map[string]string{prefillIntent: "reply"}),
			},
		},
	}, true
}

// overduePromiseMoment: WE said we would do something and the date has passed.
//
// Ours outranks theirs on purpose. A promise we owe is entirely within the
// reader's control, and it is the one they would be most embarrassed to
// discover from the other side.
func overduePromiseMoment(now time.Time, page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	claim, ok := oldestOverdueCommitment(now, page)
	if !ok {
		return crmcontracts.PersonMoment{}, false
	}
	overdue := int(now.Sub(*claim.DueAt).Hours() / 24)
	evidence := []crmcontracts.PersonMomentEvidence{{
		Type:       crmcontracts.PersonMomentEvidenceTypeActivity,
		Id:         &claim.SourceActivityId,
		Label:      claim.Body,
		Snippet:    &claim.SourceQuote,
		ObservedAt: claim.DueAt,
	}}
	return crmcontracts.PersonMoment{
		ClaimKey:            "moment:overdue_promise",
		Rule:                crmcontracts.PersonMomentRuleOverduePromise,
		RuleVersion:         ptr(ruleVersion),
		EvidenceFingerprint: fingerprintOf(evidence),
		Headline:            fmt.Sprintf("You owe them: %s", claim.Body),
		WhyNow:              fmt.Sprintf("Promised for a date that passed %d days ago.", overdue),
		Confidence:          crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence:            evidence,
		FreshnessAt:         claim.DueAt,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindDraftReply,
			Label: "Send it now",
			State: crmcontracts.PersonMomentActionStateWillConfirm,
			Destination: &crmcontracts.PersonMomentDestination{
				Surface: crmcontracts.PersonMomentDestinationSurfaceComposer,
				Prefill: prefill(map[string]string{prefillIntent: "deliver_commitment", "subject": claim.Body}),
			},
		},
	}, true
}

// goneQuietMoment: our outbound has gone unanswered past the configured rule.
//
// The moment names the rule in its own text. A reader who disagrees with the
// verdict can see what produced it, which is the difference between a system
// that judges and one that explains.
func goneQuietMoment(now time.Time, page *crmcontracts.Person360) (crmcontracts.PersonMoment, bool) {
	if page.LastOutboundAt == nil {
		return crmcontracts.PersonMoment{}, false
	}
	outbound := *page.LastOutboundAt
	if page.LastInboundAt != nil && !page.LastInboundAt.Before(outbound) {
		// They answered. Silence is not the story.
		return crmcontracts.PersonMoment{}, false
	}
	waiting := int(now.Sub(outbound).Hours() / 24)
	if waiting < goneQuietAfterDays {
		return crmcontracts.PersonMoment{}, false
	}
	quietFor := waiting
	if page.LastInboundAt != nil {
		quietFor = int(now.Sub(*page.LastInboundAt).Hours() / 24)
	}
	evidence := outboundEvidence(page, outbound)
	return crmcontracts.PersonMoment{
		ClaimKey:            "moment:gone_quiet",
		Rule:                crmcontracts.PersonMomentRuleGoneQuiet,
		RuleVersion:         ptr(ruleVersion),
		EvidenceFingerprint: fingerprintOf(evidence),
		Headline:            fmt.Sprintf("No reply for %d days", quietFor),
		WhyNow: fmt.Sprintf("Rule: outbound with no reply after %d days. Your follow-up was sent %d days ago.",
			goneQuietAfterDays, waiting),
		Confidence:  crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence:    evidence,
		FreshnessAt: &outbound,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindDraftReply,
			Label: "Draft a follow-up",
			State: crmcontracts.PersonMomentActionStateWillConfirm,
			Destination: &crmcontracts.PersonMomentDestination{
				Surface: crmcontracts.PersonMomentDestinationSurfaceComposer,
				Prefill: prefill(map[string]string{prefillIntent: "follow_up"}),
			},
		},
		SecondaryActions: &[]crmcontracts.PersonMomentAction{{
			Kind:  crmcontracts.PersonMomentActionKindAskColleague,
			Label: "Ask for context",
			State: crmcontracts.PersonMomentActionStateAvailable,
		}},
	}, true
}

// nothingNeededMoment is the quiet success state — rung 10, and the answer far
// more often than any of the others.
//
// It is a moment rather than an absence because the reader opened the page to
// be told what to do, and "nothing" is a legitimate answer that an empty card
// fails to give.
func nothingNeededMoment(now time.Time) crmcontracts.PersonMoment {
	return crmcontracts.PersonMoment{
		ClaimKey:            "moment:nothing_needed",
		Rule:                crmcontracts.PersonMomentRuleNothingNeeded,
		RuleVersion:         ptr(ruleVersion),
		EvidenceFingerprint: "quiet",
		Headline:            "Nothing needs you today",
		WhyNow:              "No meeting is close, nothing is owed, and nobody is waiting on a reply.",
		Confidence:          crmcontracts.PersonMomentConfidenceObservedFact,
		Evidence:            []crmcontracts.PersonMomentEvidence{},
		FreshnessAt:         &now,
		RecommendedAction: crmcontracts.PersonMomentAction{
			Kind:  crmcontracts.PersonMomentActionKindLogActivity,
			Label: "Log an interaction",
			State: crmcontracts.PersonMomentActionStateAvailable,
		},
	}
}
