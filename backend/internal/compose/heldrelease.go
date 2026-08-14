// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Releasing a held draft: the approve-side executor for the message an
// automation composed and a human decided to send.
//
// The whole feature turns on this file doing ONE thing — the ordinary send —
// rather than a second implementation of it. A released draft is not a special
// kind of mail: it is the same send the composer performs, reached by a
// different door, so consent, recipient visibility, the mailbox pre-flight,
// the sign-off, deliverability, the outbound activity, its delivery row and its
// dispatch job are all the ones an ordinary send produces. Anything this file
// derived for itself would be a way for a released draft to differ from the
// message the same human could have typed, and there is no version of that
// which is correct.
//
// Rejection needs nothing here. approvals.Decide already records the reason,
// and automation's own consumer lands the parked run's terminal 'blocked'
// outcome — a discard is complete without this file's involvement.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// heldDraftReleaseEffect builds the approvals.ApprovedEffect compose injects
// for kind held_draft.
//
// THREE things commit together or none of them do: the single-use redemption
// that spends the human's authority, the send itself, and the parked
// automation run's completion. RedeemAndApply owns that transaction and this
// effect only fills it.
//
// Why all three and not just the first two. Each pair left alone is a defect
// this codebase can already name. Redemption without the send is the consumed
// approval whose effect never ran — the failure editscope.go records having
// been paid for once, and unrecoverable here because a send cannot be replayed
// from a spent authority. The send without the run transition leaves history
// claiming an automation is still waiting for a decision that released a
// message days ago. And the send without the redemption is a message nothing
// records anyone approving.
//
// A gate that refuses — consent withdrawn since staging, a sender who lost
// their mailbox, an anchor archived out from under the draft — is answered
// BEFORE any of this, by heldDraftPrecheck below, so the approval is never
// decided and the human can fix the cause and approve the same row again.
// Reaching a refusal from in here means the world moved between the preflight
// and this transaction, which rolls the redemption back but leaves an approved
// row nothing can re-drive — rare by construction, and the reason the preflight
// exists rather than an accepted cost.
func heldDraftReleaseEffect(
	svc *approvals.Service,
	store *activities.Store,
	gate activities.ConsentGate,
	stager activities.DeliveryStager,
) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		proposal, err := decodeHeldDraft(proposedChange)
		if err != nil {
			return err
		}
		// The anchor comes out of the PAYLOAD, never from the approval's
		// target. The effect is not handed a target, and reaching for one would
		// be reconstructing a fact the edit scope already pinned: the anchor is
		// a UUID inside the proposed change, so a modify-then-approve edit can
		// correct the words and cannot re-aim the reply at another thread.
		origin, in := sendFromHeldDraft(proposal)
		return releaseHeldDraft(ctx, svc, store, approvalID, diffHash, origin, in, gate, stager)
	}
}

// decodeHeldDraft reads a staged proposal, refusing one carrying anything this
// release does not act on.
//
// Strictly, and the strictness is the point. ADR-0036 §4 lets a human edit a
// staged payload, and the edit scope pins only the entity references in it — so
// an API caller can add "cc" or "attachment_ids" to the change they approve.
// The approval, its audit row and its diff hash would then record a message
// with attachments, and the send would put out one without them: what the
// record says a human approved would not be what went to the recipient. A
// refusal is the only answer that keeps those two the same message.
func decodeHeldDraft(raw json.RawMessage) (automation.HeldDraftProposal, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var proposal automation.HeldDraftProposal
	if err := decoder.Decode(&proposal); err != nil {
		return automation.HeldDraftProposal{},
			fmt.Errorf("compose: this held draft carries something the release does not send: %w", err)
	}
	return proposal, nil
}

// sendFromHeldDraft turns a staged proposal into the send it describes.
//
// The anchor comes out of the PAYLOAD, never from the approval's target. The
// effect is not handed a target, and reaching for one would re-derive a fact
// the edit scope already pinned: the anchor is a UUID inside the proposed
// change, so a modify-then-approve edit can correct the words and cannot re-aim
// the reply at another thread.
//
// The addressee is NOT pinned that way — an email address is not UUID-shaped —
// so an approver editing over the API can change `to`. That is deliberate and
// it is the same authority they already hold: the release re-runs the consent
// gate against whatever address it ends up with, so an edited recipient is
// governed exactly as if they had typed the message themselves. The inbox does
// not offer the field, because reading a draft is not the moment to redirect
// it.
func sendFromHeldDraft(p automation.HeldDraftProposal) (activities.SendOrigin, activities.SendEmailInput) {
	origin := activities.FromActivity(ids.From[ids.ActivityKind](p.AnchorActivityID))
	return origin, activities.SendEmailInput{
		// One addressee, matching what staging resolved and the approver read.
		// Recipients is the merged consent list and `to` is derived from it by
		// subtracting cc/bcc — with neither present the two are the same single
		// address, which is exactly the shape intended.
		Recipients:     []string{p.To},
		Subject:        p.Subject,
		Body:           p.Body,
		ConsentPurpose: p.ConsentPurpose,
	}
}

// releaseHeldDraft prepares the send and then commits it with the redemption
// and the parked run's completion.
//
// Gates first, OUTSIDE the redemption transaction, then the write inside it.
// That is SendEmail's own boundary and it has to be: preparation reads through
// the store rather than through a transaction, so running it inside one
// acquires a second pool connection while the first is held — under load not
// slow but stuck, every connection held by a transaction waiting for another.
//
// The same reads already ran in the preflight. Running them again is not waste:
// they are live state, and the reading that decides a send is the one taken
// closest to the write. What must not move between the two is the anchor, and
// the write half re-reads that under a row lock.
func releaseHeldDraft(
	ctx context.Context,
	svc *approvals.Service,
	store *activities.Store,
	approvalID ids.ApprovalID,
	diffHash string,
	origin activities.SendOrigin,
	in activities.SendEmailInput,
	gate activities.ConsentGate,
	stager activities.DeliveryStager,
) error {
	prepared, err := store.PrepareSend(ctx, origin, in, gate, stager)
	if err != nil {
		return err
	}
	return svc.RedeemAndApply(ctx, approvalID, automation.HeldDraftKind, diffHash, func(tx pgx.Tx) error {
		if _, err := store.SendPreparedTx(ctx, tx, origin, prepared, stager); err != nil {
			return err
		}
		return automation.CompleteApprovedRunTx(ctx, tx, approvalID)
	})
}

// heldDraftPrecheck answers, before a decision commits, whether this draft
// could be sent at all.
//
// It runs the identical preparation the release runs and throws the result
// away, which is the same trick scheduleSend uses at schedule time and for the
// same reason: the human learns NOW that consent was withdrawn or the thread
// was archived, while the approval is still pending and they can act on it —
// rather than after a decision that cannot be taken back.
//
// A pure read, as the precheck contract requires: preparation renders and
// snapshots, and writes nothing.
func heldDraftPrecheck(
	store *activities.Store,
	gate activities.ConsentGate,
	stager activities.DeliveryStager,
) approvals.ReleasePrecheck {
	return func(ctx context.Context, proposedChange json.RawMessage) error {
		proposal, err := decodeHeldDraft(proposedChange)
		if err != nil {
			return err
		}
		origin, in := sendFromHeldDraft(proposal)
		_, err = store.PrepareSend(ctx, origin, in, gate, stager)
		return err
	}
}
