// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Staging the people ONE act published as decisions. The gate upstream decided
// whether the site published someone contactable; this decides whether the
// workspace needs to ASK about them, what question it asks, and — because they
// were asked by one act — that they arrive in the inbox together.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// stageSiteLeads records the published people of ONE act as thin "site_lead"
// proposals: exactly what the site printed, nothing enriched. Each person is
// decided on their own — accepting the CTO does not accept the whole roster —
// but they are ASKED together, under one bundle.
//
// One transaction for the whole set, and that is what makes the bundle mean
// what it says. Staged one commit at a time, a human refreshing their inbox
// mid-act can see the bundle, decide it, and be told the act's question is
// answered while the rest of it is still being written — and a worker that dies
// halfway leaves a permanently partial set. Neither is reachable when the
// members become visible at once.
func (w *siteDeepReadWorker) stageSiteLeads(ctx context.Context, readID ids.UUID, claim people.SiteReadClaim, found []sitePerson, bundleID ids.UUID) ([]ids.UUID, error) {
	var proposalIDs []ids.UUID
	err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		var err error
		proposalIDs, err = w.stageSiteLeadsInTx(ctx, tx, readID, claim, found, bundleID)
		return err
	})
	return proposalIDs, err
}

// stageSiteLeadsInTx is stageSiteLeads on the caller's transaction, for an act
// that stages more than leads and must commit all of it at once.
func (w *siteDeepReadWorker) stageSiteLeadsInTx(ctx context.Context, tx pgx.Tx, readID ids.UUID, claim people.SiteReadClaim, found []sitePerson, bundleID ids.UUID) ([]ids.UUID, error) {
	var proposalIDs []ids.UUID
	for _, person := range found {
		approvalID, staged, err := w.stageSiteLead(ctx, tx, readID, claim, person, bundleID)
		if err != nil {
			return nil, fmt.Errorf("staging the %s lead: %w", person.Name, err)
		}
		if !staged {
			continue
		}
		proposalIDs = append(proposalIDs, approvalID.UUID)
	}
	return proposalIDs, nil
}

// stageSiteLead records ONE published person as a thin "site_lead" proposal.
//
// It reports whether anything was staged. A person the workspace already
// knows is not a decision: they reached us by email long before a crawler
// read their name off the about page, and re-proposing them spends the
// queue on a confirmation that would land on the row that is already there.
func (w *siteDeepReadWorker) stageSiteLead(ctx context.Context, tx pgx.Tx, readID ids.UUID, claim people.SiteReadClaim, person sitePerson, bundleID ids.UUID) (ids.ApprovalID, bool, error) {
	if claim.OrganizationID == nil {
		return ids.ApprovalID{}, false, errors.New("site deep read: an unbound onboarding draft cannot stage a lead proposal")
	}
	probeCtx, err := w.probeCtx(ctx)
	if err != nil {
		return ids.ApprovalID{}, false, err
	}
	known, err := w.people.EmailAlreadyOnFileTx(probeCtx, tx, person.PublishedEmail)
	// A requester who may not read people cannot be told, on their own
	// authority, that this one is already known — so they are told nothing and
	// simply get the proposal. Suppressing it on the WORKER's authority is the
	// disclosure probeCtx exists to prevent, and failing the whole read over a
	// question that only saves a human one click is worse than asking it.
	if errors.Is(err, apperrors.ErrPermissionDenied) {
		known, err = false, nil
	}
	if err != nil {
		return ids.ApprovalID{}, false, fmt.Errorf("checking whether %s is already on file: %w", person.Name, err)
	}
	if known {
		// The skip is log-only. It is not an extraction drop — the gate already
		// passed this person — so it has no place in the dossier's drop report,
		// but it still says why a published person produced no question.
		w.log.InfoContext(ctx, "published person not proposed",
			"lane", lanePeople, "reason", dropAlreadyOnFile,
			"read", readID.String(), "url", person.SourceURL)
		return ids.ApprovalID{}, false, nil
	}
	in, err := siteLeadStageInput(readID, *claim.OrganizationID, claim.SeedURL, person, bundleID)
	if err != nil {
		return ids.ApprovalID{}, false, err
	}
	approvalID, err := w.approvals.StageOrJoinPendingInTx(ctx, tx, in)
	if err != nil {
		return ids.ApprovalID{}, false, err
	}
	return approvalID, true, nil
}

// probeCtx narrows the worker's authority to the requesting human's, for the
// one question this lane asks ABOUT existing records.
//
// The worker itself is a system principal: it writes on the requester's behalf
// and sees every row in the workspace, which is right for writing and wrong
// for asking. Whether a person is already on file decides whether a proposal
// appears in that human's inbox, so answering it workspace-wide turns the
// inbox into an existence oracle — a rep points a read at a page listing
// addresses they want to test, and every name that yields NO proposal is one
// the workspace holds, including records another team owns and every list
// endpoint hides from them. Under the requester's own scope a record they
// cannot see reads as absent, they get the proposal, and the accept path's
// natural key still collapses it onto the row that exists.
//
// A requester with no recoverable human identity (the zero uuid a non-human
// requested_by yields) gets no narrowing and no probe: the read proposes
// everyone it published, which is the pre-existing behaviour and leaves the
// decision with a human.
func (w *siteDeepReadWorker) probeCtx(ctx context.Context) (context.Context, error) {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return nil, errors.New("site deep read: no principal to scope the already-on-file probe to")
	}
	workspaceID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return nil, errors.New("site deep read: no workspace to scope the already-on-file probe to")
	}
	if actor.UserID.IsZero() {
		return ctx, nil
	}
	rbac, err := w.authority.EffectiveRBAC(ctx, workspaceID, actor.UserID)
	if err != nil {
		// A requester who has lost their grants since starting the read cannot
		// lend any scope, so nothing is suppressed on their behalf.
		if errors.Is(err, apperrors.ErrNotFound) {
			return ctx, nil
		}
		return nil, fmt.Errorf("resolving the requester's scope for the already-on-file probe: %w", err)
	}
	return principal.WithActor(ctx, principal.Principal{
		Type:        principal.PrincipalHuman,
		ID:          actor.ID,
		UserID:      actor.UserID,
		OnBehalfOf:  actor.UserID,
		TeamIDs:     rbac.TeamIDs,
		Permissions: rbac.Permissions,
	}), nil
}

// siteLeadStageInput builds the staging for one published person, INCLUDING
// the logical identity that makes a re-read of the same site supersede its own
// last undecided proposal instead of stacking beside it.
//
// The identity has to be the natural key, not the printed name. A page that
// reflows "Anna Muster" to "  anna   MUSTER " would slip past a raw-name
// identity and stack a second question about the same person; two genuinely
// different people who share a name would collapse into one, and staging the
// second would expire the first's still-undecided approval. The natural key
// normalizes the name and carries the published email, so it separates exactly
// the people the accept path keeps separate.
func siteLeadStageInput(readID, organizationID ids.UUID, seedURL string, person sitePerson, bundleID ids.UUID) (approvals.StageInput, error) {
	naturalKey := siteLeadSourceID(organizationID, person.Name, person.PublishedEmail)
	proposedChange, err := json.Marshal(siteLeadProposal{
		OrganizationID:  organizationID,
		SiteReadID:      readID,
		NaturalKey:      naturalKey,
		Name:            person.Name,
		Role:            person.Role,
		PublishedEmail:  person.PublishedEmail,
		LinkedinURL:     person.LinkedinURL,
		EvidenceSnippet: person.EvidenceSnippet,
		SourceURL:       person.SourceURL,
	})
	if err != nil {
		return approvals.StageInput{}, err
	}
	identity, err := json.Marshal(map[string]string{"natural_key": naturalKey})
	if err != nil {
		return approvals.StageInput{}, err
	}
	digest := sha256.Sum256(proposedChange)
	return approvals.StageInput{
		Kind:           siteLeadProposalKind,
		ProposedChange: proposedChange,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     enrichTargetType,
		TargetID:       organizationID,
		Identity:       identity,
		JoinPending:    true,
		BundleID:       bundleID,
		Summary:        fmt.Sprintf("Lead from %s: %s — %s", seedURL, person.Name, person.Role),
	}, nil
}
