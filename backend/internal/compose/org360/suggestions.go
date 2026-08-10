// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The next-step suggestions: what this account looks like it needs, computed
// from its own records.
//
// NO MODEL. Every rule here is a comparison a rep could make themselves, and
// each suggestion carries the rule in the words they read — so they can
// disagree with the REASON rather than with a verdict they cannot inspect.
// A model could phrase these more warmly; it could not make them checkable,
// and checkable is what makes advice actionable.
//
// Each rule runs under the same row-scope predicates as the section it concerns,
// and only when the caller holds the grant that section rides — so a suggestion
// can only ever point at records they can open, and a grant they lack produces
// silence rather than advice inferred from the gap. What the rules read, and why
// they do not read the truncated section pages, is suggestionreads.go.
//
// Nothing is staged and nothing is sent. A suggestion is a sentence and its
// evidence; what to DO about it stays the rep's move through the same endpoints
// they would have used anyway.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// noReplyDays is how long an unanswered outbound message waits before it is
// worth mentioning. Short enough to still be actionable, long enough that a
// normal reply time does not trigger it.
const noReplyDays = 7

// maxSuggestions is how many rows the card offers.
//
// A product bound, not a performance one: three is what a reader treats as
// advice, and a longer list is one they scroll past — a card nobody reads is
// worth less than a shorter one they act on (AC-company-14). What it drops is
// REPORTED in suggestions_dropped, because a silent cap reads as "that is
// everything".
//
// The cap is applied HERE and not by the client, so the dropped count and the
// rows shown can never describe different lists.
const maxSuggestions = 3

// The suggestion kinds, DERIVED from the contract's enum rather than
// re-spelled, so a rename upstream fails to compile here instead of laundering
// a hand-typed string past the type.
//
// They double as the fingerprint's leading key, which means a rename does
// invalidate the dismissals stored against the old name. That is the right
// outcome: a renamed kind is a different kind, and a rep's judgment of the old
// one is not a judgment of the new.
var (
	suggestNoReply     = crmcontracts.Organization360SuggestionKindNoReply
	suggestStalledDeal = crmcontracts.Organization360SuggestionKindStalledDeal
	suggestNoNextStep  = crmcontracts.Organization360SuggestionKindNoNextStep
	suggestConflict    = crmcontracts.Organization360SuggestionKindLifecycleConflict
)

// readSuggestions is the section.
//
// It holds no grant of its own. Each rule runs when the caller holds the grant
// its records sit behind, so a reader who can see the pipeline but not the
// timeline gets the advice they can act on and none they cannot. A caller shown
// neither has nothing to be advised from, and the section is omitted and named
// rather than answering empty.
func (a *assembly) readSuggestions() error {
	// Resolved first, and unconditionally: dismissals are per user, so a caller
	// with no user id has no suggestions surface at all. Leaving this to
	// keepUndismissed — which is skipped when no rule fires — made the section
	// present for such a caller on a quiet account and omitted on a busy one.
	if _, err := actingUser(a.ctx); err != nil {
		return err
	}
	// The SAME read the state strip used. Two readings of the newest message
	// could disagree with each other inside one page — the composite read
	// exists to make that impossible.
	in, err := a.suggestionInputsOnce()
	if err != nil {
		return err
	}
	if !in.advisable() {
		return fmt.Errorf(
			"suggestions are read from the timeline and the pipeline, and this caller may read neither: %w",
			apperrors.ErrPermissionDenied)
	}
	found, dropped, err := a.svc.suggestionsFor(a.ctx, a.tx, a.orgID, a.now, in)
	if err != nil {
		return err
	}
	a.out.Suggestions = &found
	// Set together with the list, so the count is absent exactly when the section
	// is. A zero on a section this read never computed would state "no further
	// suggestions" about an account it did not look at.
	a.out.SuggestionsDropped = &dropped
	return nil
}

// suggestionsFor runs every rule, drops what this caller has already judged,
// and caps what is left — reporting exactly how many of THEIR undismissed
// suggestions the answer does not carry.
//
// The order the rules run in IS the priority the cap applies, so it is a product
// decision rather than a consequence of how the blocks are arranged:
//
//  1. no_reply — a person is waiting on us. Nothing else on the card is someone
//     else's time.
//  2. stalled_deal, longest idle first — money that has stopped moving.
//  3. no_next_step — a gap in the plan, which the two above usually imply
//     anyway, so it is the one worth losing when the card is full.
//
// What the cap drops is reported, never shown, so a rep who never scrolls past
// the card still sees the most urgent thing on it.
func (s *Service) suggestionsFor(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time, in suggestionInputs,
) ([]crmcontracts.Organization360Suggestion, int, error) {
	found := candidateSuggestions(orgID, now, in)
	// Dismissals are applied BEFORE the cap, so judging one row reveals the next
	// instead of shrinking the card. Capping first would spend a slot on a
	// suggestion the rep has already dealt with.
	kept, err := s.keepUndismissed(ctx, tx, orgID, found)
	if err != nil {
		return nil, 0, err
	}
	if len(kept) > maxSuggestions {
		return kept[:maxSuggestions], len(kept) - maxSuggestions, nil
	}
	return kept, 0, nil
}

// candidateSuggestions is every suggestion this account raises for this caller,
// before dismissals and before the display cap.
//
// It is the definition of "what the rules produce", and both the card and the
// dismissal endpoint go through it — the card to show them, the dismissal to
// recognize the one it was handed.
func candidateSuggestions(
	orgID ids.OrganizationID, now time.Time, in suggestionInputs,
) []crmcontracts.Organization360Suggestion {
	found := make([]crmcontracts.Organization360Suggestion, 0, maxSuggestions)
	// First, because a record that contradicts itself outranks any advice about
	// what to do next: acting on a stage that is wrong is worse than not acting.
	found = appendIf(found, lifecycleConflict(orgID, in))
	if in.timeline && in.hasNewest {
		found = appendIf(found, staleThread(orgID, now, in.newest))
	}
	if in.pipeline {
		found = append(found, stalledDealSuggestions(in.open.Stalled)...)
		if in.timeline {
			// The no-next-step rule reads BOTH: the pipeline says the account is
			// live, and the task grant is what makes "nothing is scheduled" a fact
			// rather than a gap in what this caller may see.
			found = appendIf(found, noNextStepSuggestion(orgID, in))
		}
	}
	return found
}

// keepUndismissed removes the suggestions this caller has already judged.
//
// The database is asked about THESE fingerprints, not for the caller's whole
// dismissal history — so the read is bounded by the suggestions in hand.
func (s *Service) keepUndismissed(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID,
	found []crmcontracts.Organization360Suggestion,
) ([]crmcontracts.Organization360Suggestion, error) {
	if len(found) == 0 {
		return found, nil
	}
	candidates := make([]string, 0, len(found))
	for _, suggestion := range found {
		candidates = append(candidates, suggestion.Fingerprint)
	}
	dismissed, err := s.dismissedFingerprints(ctx, tx, orgID, candidates)
	if err != nil {
		return nil, err
	}
	kept := make([]crmcontracts.Organization360Suggestion, 0, len(found))
	for _, suggestion := range found {
		if dismissed[suggestion.Fingerprint] {
			continue
		}
		kept = append(kept, suggestion)
	}
	return kept, nil
}

func appendIf(
	into []crmcontracts.Organization360Suggestion, one *crmcontracts.Organization360Suggestion,
) []crmcontracts.Organization360Suggestion {
	if one == nil {
		return into
	}
	return append(into, *one)
}

// setDraftReply anchors the composer on the message that went unanswered. The
// rule already holds that activity, so the client is told which one rather than
// left to infer it from the evidence order — an ordering nobody promised.
func setDraftReply(out *crmcontracts.Organization360Suggestion, activityID ids.UUID) {
	id := openapi_types.UUID(activityID)
	out.Action = newSuggestionAction(crmcontracts.Organization360SuggestionActionKindDraftReply)
	out.Action.ActivityId = &id
}

// setOpenDeal points at the deal that stalled.
func setOpenDeal(out *crmcontracts.Organization360Suggestion, dealID ids.UUID) {
	id := openapi_types.UUID(dealID)
	out.Action = newSuggestionAction(crmcontracts.Organization360SuggestionActionKindOpenDeal)
	out.Action.DealId = &id
}

// newSuggestionAction builds the generated anonymous action struct. The shape is
// spelled here and only here: a second literal would drift the moment the
// contract gains a field.
//
//nolint:staticcheck // ST1003: the field names mirror the oapi-codegen type this must assign to
func newSuggestionAction(kind crmcontracts.Organization360SuggestionActionKind) *struct {
	ActivityId *openapi_types.UUID                              `json:"activity_id,omitempty"`
	DealId     *openapi_types.UUID                              `json:"deal_id,omitempty"`
	Kind       crmcontracts.Organization360SuggestionActionKind `json:"kind"`
} {
	action := new(struct {
		ActivityId *openapi_types.UUID                              `json:"activity_id,omitempty"`
		DealId     *openapi_types.UUID                              `json:"deal_id,omitempty"`
		Kind       crmcontracts.Organization360SuggestionActionKind `json:"kind"`
	})
	action.Kind = kind
	return action
}

// staleThread fires when the account's most recent message was OURS and nobody
// answered it.
//
// Direction is the whole rule: an unanswered outbound is a thread waiting on
// them, while an unanswered inbound is a thread waiting on US — a different
// problem with a different action, and conflating the two would tell a rep to
// chase someone who is waiting for their reply.
func staleThread(
	orgID ids.OrganizationID, now time.Time, newest lastMessage,
) *crmcontracts.Organization360Suggestion {
	// An unrecorded direction cannot support advice about who owes a reply: a
	// capture that never said who spoke is not evidence that we did.
	if newest.Direction != string(crmcontracts.ActivityDirectionOutbound) {
		return nil
	}
	waited := now.Sub(newest.At)
	if waited < noReplyDays*24*time.Hour {
		return nil
	}
	evidence := []crmcontracts.OrganizationBriefEvidence{{
		EntityType: crmcontracts.OrganizationBriefEvidenceEntityTypeActivity,
		EntityId:   openapi_types.UUID(newest.ID),
	}}
	out := &crmcontracts.Organization360Suggestion{
		Kind: suggestNoReply,
		// Channel-neutral wording: the newest exchange may have been a call or a
		// meeting, and "you wrote" would be a small false statement about it.
		Reason:      fmt.Sprintf("You reached out %d days ago and nobody has come back.", int(waited.Hours()/24)),
		Fingerprint: fingerprint(string(suggestNoReply), orgID.String(), evidence),
		Evidence:    evidence,
	}
	// The rule's own words, and the date the EVIDENCE carries — when the thread
	// went quiet. Neither reaches the fingerprint above (PO-AC-N-14).
	out.Title = ptrString(fmt.Sprintf("Follow up: no reply in %d days", int(waited.Hours()/24)))
	out.DueAt = ptrTime(newest.At)
	setDraftReply(out, newest.ID)
	return out
}

// stalledDealSuggestions raises one per stalled open deal. The stall flag is the
// deals module's own — deals.IsStalled, against its fixed 60-day window
// (StalledThresholdDays); there is no per-pipeline setting — never re-derived
// here from a date.
func stalledDealSuggestions(stalled []stalledDeal) []crmcontracts.Organization360Suggestion {
	out := make([]crmcontracts.Organization360Suggestion, 0, len(stalled))
	for _, deal := range stalled {
		evidence := []crmcontracts.OrganizationBriefEvidence{{
			EntityType: crmcontracts.OrganizationBriefEvidenceEntityTypeDeal,
			EntityId:   openapi_types.UUID(deal.ID),
		}}
		subjectType := crmcontracts.Organization360SuggestionSubjectTypeDeal
		subjectID := openapi_types.UUID(deal.ID)
		out = append(out, crmcontracts.Organization360Suggestion{
			Kind:   suggestStalledDeal,
			Reason: fmt.Sprintf("%q has had no activity long enough to count as stalled.", deal.Name),
			// The STALL is what the fingerprint identifies, not the deal — see
			// stalledDeal.episode. This stall stays dismissed; the next one is a new
			// fact about the account.
			Fingerprint: fingerprint(string(suggestStalledDeal), deal.episode(), evidence),
			SubjectType: &subjectType,
			SubjectId:   &subjectID,
			Evidence:    evidence,
			Title:       ptrString(fmt.Sprintf("Move %q — stalled", deal.Name)),
		})
		if !deal.IdleSince.IsZero() {
			out[len(out)-1].DueAt = ptrTime(deal.IdleSince)
		}
		setOpenDeal(&out[len(out)-1], deal.ID)
	}
	return out
}

// noNextStepSuggestion fires on an account that is live — it has an open
// deal — and has nobody's next action written down.
//
// It is deliberately NOT raised for a quiet account with no open deal: there
// is nothing to advance, and "you have no task on this dormant account" is
// noise the rep would learn to scroll past, which costs the whole surface its
// credibility.
func noNextStepSuggestion(
	orgID ids.OrganizationID, in suggestionInputs,
) *crmcontracts.Organization360Suggestion {
	if in.scheduled || in.open.OpenCount == 0 {
		return nil
	}
	open := in.open
	evidence := []crmcontracts.OrganizationBriefEvidence{{
		EntityType: crmcontracts.OrganizationBriefEvidenceEntityTypeOrganization,
		EntityId:   openapi_types.UUID(orgID.UUID),
	}}
	// The digest over EVERY open deal rides the fingerprint, so closing one or
	// opening another re-raises this rather than leaving a dismissal in force
	// over a pipeline the account no longer has — including a change to a deal
	// no card listed, which a fingerprint built from a fetched page would miss.
	out := &crmcontracts.Organization360Suggestion{
		Kind:        suggestNoNextStep,
		Reason:      fmt.Sprintf("%d open deal(s) here and no task saying what happens next.", open.OpenCount),
		Fingerprint: fingerprint(string(suggestNoNextStep), open.OpenDigest, evidence),
		Evidence:    evidence,
		Title:       ptrString("Set the next step"),
	}
	// No date: this rule fires on the ABSENCE of a task, and an absence has no
	// date of its own. Inventing one would make a reading into a deadline.
	// No deal named: the account has several open, and picking one for the
	// reader would be a guess dressed as advice.
	out.Action = newSuggestionAction(crmcontracts.Organization360SuggestionActionKindAddTask)
	return out
}

// lifecycleConflict fires when the account's own correspondence contradicts the
// stage it is filed under: a contract_ended signal stands while the record
// still reads as a live customer or an open opportunity.
//
// This is the failure the whole overhaul was named for. One real account held
// an email ending its contract and a page that said "Prospect", and nothing
// anywhere put those two facts next to each other.
//
// It states the conflict and does NOT resolve it. Which of the two is wrong —
// the stage, or the reading of the mail — is a judgment only the reader can
// make, and a rule that picked one would be guessing on a record that matters.
func lifecycleConflict(
	orgID ids.OrganizationID, in suggestionInputs,
) *crmcontracts.Organization360Suggestion {
	if !in.contractEnded || !liveLifecycles[in.lifecycle] {
		return nil
	}
	evidence := []crmcontracts.OrganizationBriefEvidence{{
		EntityType: crmcontracts.OrganizationBriefEvidenceEntityTypeOrganization,
		EntityId:   openapi_types.UUID(orgID.UUID),
	}}
	return &crmcontracts.Organization360Suggestion{
		Kind: suggestConflict,
		Reason: fmt.Sprintf(
			"Their correspondence says the contract ended, but this account is still filed as %s.",
			in.lifecycle),
		// Keyed on the STAGE as well as the account, so correcting the stage
		// retires this rather than leaving a dismissal in force over a record
		// that has since been fixed.
		Fingerprint: fingerprint(string(suggestConflict), in.lifecycle, evidence),
		Evidence:    evidence,
		// The conflict is named, not resolved — which of the two is wrong is the
		// reader's judgment, so the title asks rather than instructs.
		Title: ptrString("Check the stage against what they wrote"),
	}
}

// liveLifecycles are the stages a contract_ended signal contradicts. A stage
// that already reads as over — former_customer, disqualified — is not in
// conflict with the mail that says so; it is the mail's conclusion.
var liveLifecycles = map[string]bool{"prospect": true, "opportunity": true, "customer": true}

// A title and a date are OPTIONAL on the wire, so both are pointers. Spelled
// once here rather than inline, because a suggestion that carried a zero time
// would render as "due 1 January year one" — a date is either the evidence's or
// it is absent.
func ptrString(v string) *string     { return &v }
func ptrTime(v time.Time) *time.Time { return &v }

// fingerprint identifies a suggestion by what it fired ON, not by what kind
// it is.
//
// That is what lets a dismissal be both durable and self-expiring: the same
// situation stays dismissed, and a changed one raises again on its own. A
// kind-keyed dismissal would bury every future stall on the account, and the
// surface would get quieter the longer it ran regardless of what happened.
func fingerprint(kind, subject string, evidence []crmcontracts.OrganizationBriefEvidence) string {
	parts := []string{kind, subject}
	for _, cited := range evidence {
		parts = append(parts, string(cited.EntityType)+":"+cited.EntityId.String())
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
