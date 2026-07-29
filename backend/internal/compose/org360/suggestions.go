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
// Each suggestion is derived from the 360 the caller already read, so it can
// only ever point at records that caller can open. Nothing is staged and
// nothing is sent: the actions offered are the same governed endpoints the
// rep would have used anyway.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// noReplyDays is how long an unanswered outbound message waits before it is
// worth mentioning. Short enough to still be actionable, long enough that a
// normal reply time does not trigger it.
const noReplyDays = 7

// suggestionKinds, spelled once — the contract's enum and the keys the
// dismissal fingerprints are built from.
const (
	suggestNoReply     = "no_reply"
	suggestStalledDeal = "stalled_deal"
	suggestNoNextStep  = "no_next_step"
)

// suggestionsFor derives the account's next steps from what the caller can
// see, then drops the ones this caller has already judged.
//
// It reads the assembled view rather than the database: a suggestion about a
// record the caller cannot open would be advice they cannot take, and
// deriving from the gated read makes that impossible rather than merely
// unlikely.
// readSuggestions is the section. It rides the ACTIVITY grant because every
// rule reads the timeline or the deals hanging off it: a caller who sees
// neither has nothing to be advised about, and a suggestion assembled without
// that grant would be derived from an absence rather than from records.
func (a *assembly) readSuggestions() error {
	if err := auth.Require(a.ctx, "activity", principal.ActionRead); err != nil {
		return err
	}
	suggestions, err := a.suggestionsFor()
	if err != nil {
		return err
	}
	a.out.Suggestions = &suggestions
	return nil
}

func (a *assembly) suggestionsFor() ([]crmcontracts.Organization360Suggestion, error) {
	orgID, view := a.orgID, a.out
	found := make([]crmcontracts.Organization360Suggestion, 0, 3)
	found = appendIf(found, staleThreadSuggestion(orgID, a.now, view))
	found = append(found, stalledDealSuggestions(view)...)
	found = appendIf(found, noNextStepSuggestion(orgID, view))
	if len(found) == 0 {
		return found, nil
	}
	dismissed, err := a.svc.dismissedFingerprints(a.ctx, a.tx, orgID)
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

// staleThreadSuggestion fires when the account's most recent message was
// OURS and nobody answered it.
//
// Direction is the whole rule: an unanswered outbound is a thread waiting on
// them, while an unanswered inbound is a thread waiting on US — which is a
// different problem with a different action, and conflating the two would
// tell a rep to chase someone who is waiting for their reply.
func staleThreadSuggestion(
	orgID ids.OrganizationID, now time.Time, view *crmcontracts.Organization360,
) *crmcontracts.Organization360Suggestion {
	if view.Activities == nil || len(view.Activities.Data) == 0 {
		return nil
	}
	// The timeline is newest-first, so the first message-shaped activity is
	// the last thing that happened on this account.
	for _, activity := range view.Activities.Data {
		if !isMessage(activity.Kind) {
			continue
		}
		if activity.Direction == nil || *activity.Direction != crmcontracts.ActivityDirectionOutbound {
			return nil // they spoke last, or the direction is unknown
		}
		waited := now.Sub(activity.OccurredAt)
		if waited < noReplyDays*24*time.Hour {
			return nil
		}
		evidence := []crmcontracts.OrganizationBriefEvidence{{
			EntityType: crmcontracts.OrganizationBriefEvidenceEntityTypeActivity,
			EntityId:   activity.Id,
		}}
		return &crmcontracts.Organization360Suggestion{
			Kind:        crmcontracts.Organization360SuggestionKind(suggestNoReply),
			Reason:      fmt.Sprintf("You wrote %d days ago and nobody has replied.", int(waited.Hours()/24)),
			Fingerprint: fingerprint(suggestNoReply, orgID.String(), evidence),
			Evidence:    evidence,
		}
	}
	return nil
}

// isMessage is the set of activity kinds that constitute a two-way exchange.
// A note or a task is something WE wrote to ourselves; nobody owes a reply to
// it, so it can neither start nor end a wait.
func isMessage(kind crmcontracts.ActivityKind) bool {
	switch kind {
	case crmcontracts.ActivityKindEmail, crmcontracts.ActivityKindWhatsapp, crmcontracts.ActivityKindTelegram:
		return true
	default:
		return false
	}
}

// stalledDealSuggestions raises one per stalled open deal. The stall flag is
// the server's — computed against the pipeline's own window — never
// re-derived here from a date.
func stalledDealSuggestions(view *crmcontracts.Organization360) []crmcontracts.Organization360Suggestion {
	if view.Deals == nil {
		return nil
	}
	out := make([]crmcontracts.Organization360Suggestion, 0)
	for _, deal := range view.Deals.Data {
		if !deal.Stalled {
			continue
		}
		evidence := []crmcontracts.OrganizationBriefEvidence{{
			EntityType: crmcontracts.OrganizationBriefEvidenceEntityTypeDeal,
			EntityId:   deal.DealId,
		}}
		subjectType := crmcontracts.Organization360SuggestionSubjectTypeDeal
		subjectID := deal.DealId
		out = append(out, crmcontracts.Organization360Suggestion{
			Kind:        crmcontracts.Organization360SuggestionKind(suggestStalledDeal),
			Reason:      fmt.Sprintf("%q has had no activity long enough to count as stalled.", deal.Name),
			Fingerprint: fingerprint(suggestStalledDeal, deal.DealId.String(), evidence),
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
	orgID ids.OrganizationID, view *crmcontracts.Organization360,
) *crmcontracts.Organization360Suggestion {
	if view.NextSteps == nil || len(view.NextSteps.Data) > 0 {
		return nil
	}
	if view.Deals == nil || len(view.Deals.Data) == 0 {
		return nil
	}
	evidence := []crmcontracts.OrganizationBriefEvidence{{
		EntityType: crmcontracts.OrganizationBriefEvidenceEntityTypeOrganization,
		EntityId:   openapi_types.UUID(orgID.UUID),
	}}
	// The open deals ride the fingerprint, so closing one and opening another
	// re-raises this rather than leaving a stale dismissal in force.
	var deals []string
	for _, deal := range view.Deals.Data {
		deals = append(deals, deal.DealId.String())
	}
	return &crmcontracts.Organization360Suggestion{
		Kind: crmcontracts.Organization360SuggestionKind(suggestNoNextStep),
		Reason: fmt.Sprintf("%d open deal(s) here and no task saying what happens next.",
			len(view.Deals.Data)),
		Fingerprint: fingerprint(suggestNoNextStep, strings.Join(deals, ","), evidence),
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
