// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// One live authority object per identical agent call.
//
// A refused 🟡 agent call is not a proposal a worker re-derives; it is a
// QUESTION an agent asks and then asks again, because the answer it gets back —
// "a human has to decide this" — is indistinguishable from the answer it got the
// first time. Every gate stager used to mint a fresh approval for each attempt,
// so one enrichment collected four approvals against the same organization at
// the same version with the same diff hash, a human answered all four, and not
// one of them was ever spent: the agent was holding the id of a row it had
// already been told was invalid, and each retry that arrived before a human
// clicked sent it back to stage another.
//
// So the engine answers the question the caller is actually asking — "what is
// the authority object for THIS call" — rather than the one the old signature
// implied. Two rows can already be the same question, and both cases are
// handled here: a PENDING one is joined (stageOrJoinPendingInTx, the path every
// engine stager has always taken), and an APPROVED, unspent one is handed back
// so the agent redeems the decision it already has instead of asking for a
// second one.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// StageAgentCall answers the live authority object for one refused 🟡 agent
// call, staging one only when the call does not already have one. It reports
// whether that object is ALREADY APPROVED, which is the difference between
// telling the agent to wait for a human and telling it to spend what it holds.
//
// It is the one entry point every agent-gate stager uses — the MCP registry's
// refusal branch, the per-field split on both doors, and the REST gate's — so
// the invariant is a property of the engine rather than of the callers that
// remembered. Engine stagers (a website read, a nightly sweep) keep Stage: they
// propose a CHANGE a human accepts, not a call an agent retries, and nothing
// re-presents their id.
func (s *Service) StageAgentCall(ctx context.Context, in StageInput) (ids.ApprovalID, bool, error) {
	// A call's identity IS its diff hash — computed by every gate stager over the
	// canonicalized call the redemption re-hashes — so a logical Identity has
	// nothing to name here, and honouring one would key supersession on something
	// no gate declares. Refused rather than dropped: silently ignoring it would
	// leave a caller believing its stale proposals were being superseded.
	if len(in.Identity) > 0 {
		return ids.ApprovalID{}, false, errors.New(
			"crmapprovals: an agent call is identified by its diff hash, so StageAgentCall takes no Identity")
	}
	p, ok := principal.Actor(ctx)
	if !ok {
		return ids.ApprovalID{}, false, errors.New("crmapprovals: no actor bound to context")
	}
	var (
		id       ids.ApprovalID
		approved bool
	)
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		wsID, ok := principal.WorkspaceID(ctx)
		if !ok {
			return errors.New("crmapprovals: no workspace bound to context")
		}
		// Before the probe, for the reason StageUnlessDeclined takes it before
		// its own: an empty result is not "no offer can appear". Two attempts
		// reading concurrently would both find nothing released and both stage.
		if err := lockProposalIdentity(ctx, tx, wsID, in); err != nil {
			return err
		}
		released, found, err := s.releasedApprovalInTx(ctx, tx, in, p)
		if err != nil {
			return err
		}
		if found {
			id, approved = released, true
			return nil
		}
		// stageOrJoinPendingInTx joins unconditionally — the JoinPending flag
		// selects this path in Stage and is not consulted once inside it — so a
		// second attempt at an undecided call lands on the row already waiting.
		id, err = s.stageOrJoinPendingInTx(ctx, tx, in)
		return err
	})
	return id, approved, err
}

// releasedApprovalInTx answers an approval this caller could redeem for this
// exact call right now, if one exists.
//
// "Could redeem" is asked of the redemption itself (validateRedemption +
// validateRedemptionTarget) rather than re-expressed as a wider WHERE clause,
// because a second spelling of that predicate is a second answer to it: one that
// admits a token the redemption then refuses hands the agent a dead id and buys
// exactly the loop this file exists to close. The SQL narrows to the rows that
// could plausibly qualify; the redemption decides.
func (s *Service) releasedApprovalInTx(ctx context.Context, tx pgx.Tx, in StageInput, p principal.Principal) (ids.ApprovalID, bool, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM approval
		 WHERE kind = $1 AND target_entity_id IS NOT DISTINCT FROM $2 AND diff_hash = $3
		   AND status = $4 AND consumed_at IS NULL
		 `+lockOrder+`
		 FOR UPDATE`, in.Kind, nullUUID(in.TargetID), in.DiffHash, approvalStatusApproved)
	if err != nil {
		return ids.ApprovalID{}, false, fmt.Errorf("lock the released approvals for this call: %w", err)
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowTo[ids.ApprovalID])
	if err != nil {
		return ids.ApprovalID{}, false, fmt.Errorf("read the released approvals for this call: %w", err)
	}
	for _, id := range candidates {
		a, err := get(ctx, tx, id)
		if err != nil {
			return ids.ApprovalID{}, false, fmt.Errorf("read released approval %s: %w", id, err)
		}
		spendable, err := s.redeemableNowInTx(ctx, tx, a, p, in.Kind, in.DiffHash)
		if err != nil {
			return ids.ApprovalID{}, false, err
		}
		if spendable {
			return id, true, nil
		}
	}
	return ids.ApprovalID{}, false, nil
}

// redeemableNowInTx reports whether this caller's next retry would spend this
// approval. A refusal is a VERDICT and not a failure — a decision whose
// redemption window has closed, or a pin the target has moved past, means this
// call needs a fresh question — so those answer false; anything that means the
// engine could not tell propagates, because staging a duplicate on the strength
// of an unreadable target is the failure mode, not the safe default.
func (s *Service) redeemableNowInTx(ctx context.Context, tx pgx.Tx, a row, p principal.Principal, tool, diffHash string) (bool, error) {
	if err := validateRedemption(a, p, tool, diffHash, s.now()); err != nil {
		if errors.Is(err, apperrors.ErrApprovalTokenInvalid) || errors.Is(err, apperrors.ErrRequiresApproval) {
			return false, nil
		}
		return false, err
	}
	if err := validateRedemptionTarget(ctx, tx, a); err != nil {
		if errors.Is(err, apperrors.ErrVersionSkew) || errors.Is(err, apperrors.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
