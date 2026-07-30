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
// Each rule runs under the same row-scope predicates as the section it
// concerns, and only when that section reached this caller — so a suggestion
// can only ever point at records they can open, and a withheld section
// produces silence rather than advice inferred from the gap. What the rules
// read, and why they do not read the truncated section pages, is
// suggestionreads.go.
//
// Nothing is staged and nothing is sent. A suggestion is a sentence and its
// evidence; what to DO about it stays the rep's move through the same endpoints
// they would have used anyway.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

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
// A product bound, not a performance one: advice past a handful is a list a rep
// learns to scroll past, and a card nobody reads is worth less than a shorter
// one they act on. What it drops is REPORTED in suggestions_dropped, because a
// silent cap reads as "that is everything".
const maxSuggestions = 5

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
)

// readSuggestions is the section.
//
// It holds no grant of its own. Advice is derived from the timeline and the
// pipeline, and both of those already refused or answered under their own
// gates: whichever reached this caller is what the rules may read, and a caller
// shown NEITHER has nothing to be advised from, so the section is omitted and
// named. Requiring one fixed grant here instead would withhold stalled-deal
// advice from a caller who can read deals but not activities — advice they are
// entitled to and can act on.
func (a *assembly) readSuggestions() error {
	if a.out.Activities == nil && a.out.Deals == nil {
		return fmt.Errorf(
			"suggestions are read from the timeline and the pipeline, and this caller may read neither: %w",
			apperrors.ErrPermissionDenied)
	}
	found, dropped, err := a.suggestionsFor()
	if err != nil {
		return err
	}
	a.out.Suggestions = &found
	a.out.SuggestionsDropped = dropped
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
func (a *assembly) suggestionsFor() ([]crmcontracts.Organization360Suggestion, int, error) {
	found := make([]crmcontracts.Organization360Suggestion, 0, maxSuggestions)

	// The timeline reached this caller, so the no-reply rule can run.
	if a.out.Activities != nil {
		stale, err := a.staleThreadSuggestion()
		if err != nil {
			return nil, 0, err
		}
		found = appendIf(found, stale)
	}

	// Both deal-shaped rules need the pipeline. An absent deals section means
	// the caller may not read deals at all, and advice about a pipeline they
	// cannot see is advice they cannot take.
	if a.out.Deals != nil {
		open, err := openPipeline(a.ctx, a.tx, a.orgID, a.now)
		if err != nil {
			return nil, 0, err
		}
		found = append(found, stalledDealSuggestions(open.Stalled)...)
		found = appendIf(found, noNextStepSuggestion(a.orgID, a.out, open))
	}

	// Dismissals are applied BEFORE the cap, so judging one row reveals the next
	// instead of shrinking the card. Capping first would spend a slot on a
	// suggestion the rep has already dealt with.
	kept, err := a.keepUndismissed(found)
	if err != nil {
		return nil, 0, err
	}
	if len(kept) > maxSuggestions {
		return kept[:maxSuggestions], len(kept) - maxSuggestions, nil
	}
	return kept, 0, nil
}

// keepUndismissed removes the suggestions this caller has already judged.
//
// The database is asked about THESE fingerprints, not for the caller's whole
// dismissal history — so the read is bounded by the suggestions in hand rather
// than by how many rows the table has accumulated.
func (a *assembly) keepUndismissed(
	found []crmcontracts.Organization360Suggestion,
) ([]crmcontracts.Organization360Suggestion, error) {
	if len(found) == 0 {
		return found, nil
	}
	candidates := make([]string, 0, len(found))
	for _, suggestion := range found {
		candidates = append(candidates, suggestion.Fingerprint)
	}
	dismissed, err := a.svc.dismissedFingerprints(a.ctx, a.tx, a.orgID, candidates)
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

// staleThreadSuggestion reads the account's newest message and applies the
// rule to it.
func (a *assembly) staleThreadSuggestion() (*crmcontracts.Organization360Suggestion, error) {
	newest, found, err := newestMessage(a.ctx, a.tx, a.orgID)
	if err != nil {
		return nil, err
	}
	if !found {
		// Nobody has ever exchanged a message with this account: no wait to
		// report, and no error either.
		return nil, nil //nolint:nilnil // found reports the absence; the caller appends nothing
	}
	return staleThread(a.orgID, a.now, newest), nil
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
	return &crmcontracts.Organization360Suggestion{
		Kind: suggestNoReply,
		// Channel-neutral wording: the newest exchange may have been a call or a
		// meeting, and "you wrote" would be a small false statement about it.
		Reason:      fmt.Sprintf("You reached out %d days ago and nobody has come back.", int(waited.Hours()/24)),
		Fingerprint: fingerprint(string(suggestNoReply), orgID.String(), evidence),
		Evidence:    evidence,
	}
}

// stalledDealSuggestions raises one per stalled open deal. The stall flag is
// the deals module's own (deals.IsStalled, against the pipeline's window),
// never re-derived here from a date.
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
			Kind:        suggestStalledDeal,
			Reason:      fmt.Sprintf("%q has had no activity long enough to count as stalled.", deal.Name),
			Fingerprint: fingerprint(string(suggestStalledDeal), deal.ID.String(), evidence),
			SubjectType: &subjectType,
			SubjectId:   &subjectID,
			Evidence:    evidence,
		})
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
	orgID ids.OrganizationID, view *crmcontracts.Organization360, open pipeline,
) *crmcontracts.Organization360Suggestion {
	present, scheduled := openTasks(view)
	if !present || scheduled || open.OpenCount == 0 {
		return nil
	}
	evidence := []crmcontracts.OrganizationBriefEvidence{{
		EntityType: crmcontracts.OrganizationBriefEvidenceEntityTypeOrganization,
		EntityId:   openapi_types.UUID(orgID.UUID),
	}}
	// The digest over EVERY open deal rides the fingerprint, so closing one or
	// opening another re-raises this rather than leaving a dismissal in force
	// over a pipeline the account no longer has — including a change to a deal
	// no card listed, which a fingerprint built from a fetched page would miss.
	return &crmcontracts.Organization360Suggestion{
		Kind:        suggestNoNextStep,
		Reason:      fmt.Sprintf("%d open deal(s) here and no task saying what happens next.", open.OpenCount),
		Fingerprint: fingerprint(string(suggestNoNextStep), open.OpenDigest, evidence),
		Evidence:    evidence,
	}
}

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
