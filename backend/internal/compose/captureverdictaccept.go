// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The `unsure` disposition's review queue (ADR-0072/A118 §4): when the verdict
// engine cannot answer above the confidence floor, it asks a human instead of
// guessing — and the offer it stages is deliberately lopsided.
//
// ACCEPT creates the records capture withheld. REJECT does nothing at all: no
// records, no hiding, no redaction — the mail stays exactly where it is and the
// disposition simply records that a human declined to make it a counterparty.
// That asymmetry is what keeps the approvals engine approve-only-effects: a
// proposal may only ever ADD, so a stale or mistakenly-rejected offer can never
// destroy anything.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	// counterpartyProposalKind names the staged offer in the review queue.
	counterpartyProposalKind = "capture_counterparty"
	// counterpartyTargetType is what the proposal points at: the captured
	// message, not the sender — the sender has no record yet, which is the
	// whole question being asked.
	counterpartyTargetType = "activity"
)

// counterpartyProposal is the staged offer's payload: enough to create the
// records on accept, and enough for a human to recognize the sender.
//
// It carries the DISPOSITION id rather than only the address, because accepting
// must resolve the very row that raised the question — an address alone would
// let a stale proposal resolve a newer question about the same sender.
type counterpartyProposal struct {
	DispositionID ids.UUID `json:"disposition_id"`
	Email         string   `json:"email"`
	DisplayName   string   `json:"display_name"`
	Domain        string   `json:"domain"`
	OwnerID       ids.UUID `json:"owner_id"`
	ActivityID    ids.UUID `json:"activity_id"`
}

// stageCounterpartyReview offers one unresolvable sender to a human. Returns the
// staged proposal's id so the ledger row can point at it — a re-run then finds
// the existing offer instead of stacking another copy in the inbox.
func stageCounterpartyReview(ctx context.Context, svc *approvals.Service, row capture.PendingCounterparty) (ids.UUID, error) {
	proposal := counterpartyProposal{
		DispositionID: row.ID,
		Email:         row.Email,
		DisplayName:   row.DisplayName,
		Domain:        row.Domain,
		OwnerID:       row.OwnerID,
		ActivityID:    row.ActivityID,
	}
	body, err := json.Marshal(proposal)
	if err != nil {
		return ids.Nil, fmt.Errorf("compose: encoding the counterparty proposal: %w", err)
	}
	digest := sha256.Sum256(body)
	id, err := svc.Stage(ctx, approvals.StageInput{
		Kind:           counterpartyProposalKind,
		ProposedChange: body,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     counterpartyTargetType,
		TargetID:       row.ActivityID,
		Summary:        "Is " + row.Email + " a contact worth keeping?",
		// The verdict pass is an at-least-once worker: a retried batch must
		// return the existing offer rather than multiply inbox rows.
		JoinPending: true,
	})
	if err != nil {
		return ids.Nil, err
	}
	return id.UUID, nil
}

// counterpartyAcceptEffect builds the approvals.ApprovedEffect for kind
// "capture_counterparty": the human said yes, so the records capture withheld
// are created and the disposition is closed as `real` in the SAME transaction.
//
// There is no matching reject effect, by design. Rejection is the absence of an
// effect — the approvals engine only ever runs the approved branch, which is
// exactly why an offer whose accept merely ADDS records is safe to leave sitting
// in an inbox indefinitely.
func counterpartyAcceptEffect(svc *approvals.Service, store *people.Store, pending *capture.PendingStore) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		var proposal counterpartyProposal
		if err := json.Unmarshal(proposedChange, &proposal); err != nil {
			return fmt.Errorf("compose: decoding the counterparty proposal: %w", err)
		}
		decider, ok := principal.Actor(ctx)
		if !ok {
			return fmt.Errorf("compose: counterparty accept without a deciding principal")
		}
		// The records are created by the accept executor on behalf of the human
		// who released it: their approval is on the decision's own audit row,
		// while this write carries the machine provenance that says the records
		// came from a captured message rather than someone typing them in.
		execCtx := principal.WithActor(ctx, principal.Principal{
			Type:       principal.PrincipalSystem,
			ID:         "agent:" + counterpartyProposalKind,
			UserID:     decider.UserID,
			OnBehalfOf: decider.UserID,
		})
		return svc.RedeemAndApply(ctx, approvalID, counterpartyProposalKind, diffHash, func(tx pgx.Tx) error {
			return applyCounterpartyAccept(execCtx, tx, store, pending, proposal)
		})
	}
}

// applyCounterpartyAccept creates the counterparty and closes its disposition.
// Both on the redemption's transaction, so the ledger can never read `real`
// without the records, nor the records exist under a still-open question.
func applyCounterpartyAccept(ctx context.Context, tx pgx.Tx, store *people.Store,
	pending *capture.PendingStore, proposal counterpartyProposal,
) error {
	_, err := store.EnsureCounterpartyTx(ctx, tx, people.EnsureCounterpartyInput{
		Email:       proposal.Email,
		DisplayName: proposal.DisplayName,
		Domain:      proposal.Domain,
		OwnerID:     proposal.OwnerID,
		ActivityID:  ids.From[ids.ActivityKind](proposal.ActivityID),
		Source:      counterpartyProposalKind,
		CapturedBy:  counterpartyProposalKind,
	})
	// An address erased while the offer sat in the inbox creates nothing;
	// erasure outranks an approval, and the question is still answered.
	if err != nil && !errors.Is(err, people.ErrCounterpartySuppressed) {
		return err
	}
	return pending.ResolveReviewed(ctx, tx, proposal.DispositionID, capture.PendingStatusReal,
		"accepted in the review queue")
}
