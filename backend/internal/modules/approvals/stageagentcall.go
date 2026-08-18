// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// One live authority object per identical agent call.
//
// A refused 🟡 agent call is not a proposal a worker re-derives; it is a
// QUESTION an agent asks and then asks again, because the answer it gets back —
// "a human has to decide this" — is indistinguishable from the answer it got the
// first time. A stager that mints a fresh approval per attempt therefore turns
// one act into an inbox full of identical questions, each of which a human has to
// answer separately and only one of which any retry can spend.
//
// So the engine answers the question the caller is actually asking — "what is
// the authority object for THIS call" — rather than "record a new one". Two rows
// can already be the same question, and both cases are handled here under ONE
// predicate: a PENDING one is joined, and an APPROVED, unspent one is handed back
// so the agent redeems the decision it already has instead of asking for a second
// one.

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
		live, found, err := s.liveApprovalForCallInTx(ctx, tx, in, p)
		if err != nil {
			return err
		}
		if found {
			id, approved = live.id, live.approved
			return nil
		}
		id, err = s.insertProposalInTx(ctx, tx, in)
		return err
	})
	return id, approved, err
}

// liveAuthority is one existing approval this call may use: its id, and whether
// a human has already approved it. The two travel together because a caller
// cannot say anything true to the agent with only the id (see StageCall).
type liveAuthority struct {
	id       ids.ApprovalID
	approved bool
}

// liveApprovalForCallInTx answers the approval THIS caller already has for THIS
// call — approved and unspent if there is one, otherwise the undecided one still
// sitting in the inbox.
//
// ONE predicate governs both halves, and that is why the undecided half is not
// delegated to stageOrJoinPendingInTx. That function joins any pending row of the
// kind against the target, whatever credential staged it — right for an engine
// stager, whose proposal is about a RECORD and belongs to no credential, and
// wrong for a call, which is an act one passport is authorized to perform.
// Joining across credentials would hand passport B the id staged by passport A:
// B cannot redeem it, since the redemption checks the binding, so it loops on a
// dead id — and a caller with NO passport CAN redeem it, because the redemption
// enforces that binding only against a caller presenting one. Volunteering
// somebody else's authority object is not something a deduplication may do.
//
// So the credential is in the predicate, IS NOT DISTINCT FROM: a passport is
// offered only its own approvals, a passport-less caller only passport-less ones.
// That is strictly narrower than the redemption in both directions, which is what
// makes the id handed back one the caller can actually spend.
// target_entity_type is there for the same reason target_entity_id is — the pair
// is the discriminated reference, and matching half of it would let two types
// that share an id answer for each other.
//
// Approved beats undecided when both exist, because the agent can act on an
// approved one immediately. Among equals the canonical order takes the OLDEST,
// which for the undecided half is the card the human has had in front of them
// longest.
func (s *Service) liveApprovalForCallInTx(ctx context.Context, tx pgx.Tx, in StageInput, p principal.Principal) (liveAuthority, bool, error) {
	// FOR UPDATE in the canonical order, so finding a row and a decision or
	// redemption landing on it are ordered rather than interleaved. The identity
	// lock above covers what a row lock cannot: an empty result locks nothing, and
	// two attempts that both read before either writes would both stage.
	rows, err := tx.Query(ctx, `SELECT id, status FROM approval
		 WHERE kind = $1 AND diff_hash = $2
		   AND target_entity_id IS NOT DISTINCT FROM $3
		   AND target_entity_type IS NOT DISTINCT FROM $4
		   AND passport_id IS NOT DISTINCT FROM $5
		   AND ((status = $6 AND expires_at > now()) OR (status = $7 AND consumed_at IS NULL))
		 `+lockOrder+`
		 FOR UPDATE`,
		in.Kind, in.DiffHash, nullUUID(in.TargetID), nullStr(in.TargetType), nullUUID(p.PassportID),
		statusPending, approvalStatusApproved)
	if err != nil {
		return liveAuthority{}, false, fmt.Errorf("lock the live approvals for this call: %w", err)
	}
	type candidate struct {
		ID     ids.ApprovalID
		Status string
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowToStructByPos[candidate])
	if err != nil {
		return liveAuthority{}, false, fmt.Errorf("read the live approvals for this call: %w", err)
	}
	var undecided ids.ApprovalID
	for _, c := range candidates {
		if c.Status == statusPending {
			if undecided.IsZero() {
				undecided = c.ID
			}
			continue
		}
		spendable, err := s.spendableApprovalInTx(ctx, tx, c.ID, p, in)
		if err != nil {
			return liveAuthority{}, false, err
		}
		if spendable {
			return liveAuthority{id: c.ID, approved: true}, true, nil
		}
	}
	if !undecided.IsZero() {
		return liveAuthority{id: undecided}, true, nil
	}
	return liveAuthority{}, false, nil
}

// spendableApprovalInTx reports whether this caller's next retry would actually
// consume this approved approval.
//
// "Could redeem" is asked of the redemption itself rather than re-expressed as a
// second predicate, because a second spelling is a second answer: one that admits
// a token the redemption then refuses hands the agent a dead id and buys exactly
// the loop this file exists to close.
func (s *Service) spendableApprovalInTx(ctx context.Context, tx pgx.Tx, id ids.ApprovalID, p principal.Principal, in StageInput) (bool, error) {
	a, err := get(ctx, tx, id)
	if err != nil {
		return false, fmt.Errorf("read the approved approval %s for this call: %w", id, err)
	}
	return s.redeemableNowInTx(ctx, tx, a, p, in.Kind, in.DiffHash)
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
