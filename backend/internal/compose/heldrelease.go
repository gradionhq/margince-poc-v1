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
// their mailbox, an anchor archived out from under the draft — returns an error
// from inside the transaction, so the redemption rolls back with it and the
// approval stays unconsumed and releasable once the cause is fixed. That is the
// reason the gates run INSIDE this transaction rather than before it: ordering
// them first gets the same answer on the happy path and the wrong one on every
// crash between the two.
func heldDraftReleaseEffect(
	svc *approvals.Service,
	store *activities.Store,
	gate activities.ConsentGate,
	stager activities.DeliveryStager,
) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		var proposal automation.HeldDraftProposal
		if err := json.Unmarshal(proposedChange, &proposal); err != nil {
			return fmt.Errorf("compose: decoding a held draft for release: %w", err)
		}
		// The anchor comes out of the PAYLOAD, never from the approval's
		// target. The effect is not handed a target, and reaching for one would
		// be reconstructing a fact the edit scope already pinned: the anchor is
		// a UUID inside the proposed change, so a modify-then-approve edit can
		// correct the words and cannot re-aim the reply at another thread.
		origin := activities.FromActivity(ids.From[ids.ActivityKind](proposal.AnchorActivityID))
		in := activities.SendEmailInput{
			// One addressee, matching what staging resolved and the approver
			// read. Recipients is the merged consent list and `to` is derived
			// from it by subtracting cc/bcc — with neither present the two are
			// the same single address, which is exactly the shape intended.
			Recipients:     []string{proposal.To},
			Subject:        proposal.Subject,
			Body:           proposal.Body,
			ConsentPurpose: proposal.ConsentPurpose,
		}
		return svc.RedeemAndApply(ctx, approvalID, automation.HeldDraftKind, diffHash, func(tx pgx.Tx) error {
			if _, err := store.SendEmailInTx(ctx, tx, origin, in, gate, stager); err != nil {
				return err
			}
			return automation.CompleteApprovedRunTx(ctx, tx, approvalID)
		})
	}
}
