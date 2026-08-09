// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// What POLLING a staged proposal needs, as distinct from what redeeming one
// needs.
//
// The MCP Tasks extension hands an agent a durable handle while a person
// decides, and polling that handle asks two questions this module had no
// read-only answer for: has anyone decided yet, and — once they have — what
// exactly did they release? Both are reads, and neither settles anything: the
// decision they report still has to be redeemed, single-use, through Redeem.
//
// EVERY METHOD HERE IS BOUND TO THE PASSPORT THAT STAGED THE PROPOSAL, which is
// this file's gate and the reason it needs no other. These are not a human's
// view of an inbox — that is inbox.go, scoped to what a person may decide —
// they are an agent's view of its OWN proposal, and an agent has exactly one:
// the one it staged. So the binding is the whole authorization question, and
// answering it in the SQL rather than above it means no caller can be added
// later that forgets to ask.
//
// A proposal staged by a different passport, or by a human, is reported ABSENT
// rather than forbidden — the existence-hiding answer the row-scope rule gives
// everywhere else, because "that approval exists but is not yours" is an oracle.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The effective statuses TaskState answers with, exported so a caller can map
// them without spelling the words again. A second spelling is a second
// vocabulary, and the two drift the first time either side moves — which for a
// status a poll branches on means an approved proposal read as still pending.
//
// StatusExpired is spelled here rather than aliased because it is not a stored
// value: the status CHECK knows nothing about it, and effectiveStatus derives
// it from a pending row past its window. The two places that DID spell it
// (effectiveStatus, the bundle outcome probe) read this constant now, so the
// derived status has one definition like the stored three.
const (
	StatusPending  = statusPending
	StatusApproved = approvalStatusApproved
	StatusRejected = approvalStatusRejected
	StatusExpired  = "expired"
)

// TaskState is the live decision state of one staged approval.
type TaskState struct {
	// Status is the EFFECTIVE one, so a pending row past its window reads as
	// expired here exactly as it does on every other surface. A poll told
	// "pending" about a dead proposal would wait forever.
	Status string
	// ExpiresAt is the last moment this proposal can still be ACTED on, which
	// is not always the staging deadline.
	//
	// A pending row's own expires_at is when the offer lapses; but a human who
	// approves one second before that still leaves a decision redeemable for
	// RedemptionWindow afterwards. A handle that expired at the staging
	// deadline would refuse itself while its approval was live, stranding an
	// effect the person had already released — so the answer is the later of
	// the two, per status.
	ExpiresAt time.Time
	// Consumed reports that the single-use authority has already been spent.
	Consumed bool
}

// TaskState answers whether this agent's own proposal has been decided, and how
// long it remains worth waiting on.
func (s *Service) TaskState(ctx context.Context, id ids.ApprovalID) (TaskState, error) {
	var state TaskState
	err := s.readOwnProposal(ctx, id, func(a row) {
		status := a.effectiveStatus(s.now())
		state = TaskState{Status: status, ExpiresAt: actionableUntil(a, status), Consumed: a.ConsumedAt != nil}
	})
	if err != nil {
		return TaskState{}, err
	}
	return state, nil
}

// actionableUntil answers the last moment this proposal can still be acted on.
//
// A DECIDED row is bounded by its own decision: the yes is spendable for
// RedemptionWindow after decided_at and worthless afterwards. A row still
// awaiting a decision is bounded by the latest either could happen — somebody
// may approve it in its final second, and the redemption window runs from
// there — so the honest deadline for a handle to it is the staging window plus
// that one.
//
// Nothing here extends what a redemption will ACCEPT. validateRedemption is the
// authority; this only stops a handle from expiring before it.
func actionableUntil(a row, status string) time.Time {
	if a.DecidedAt != nil {
		return a.DecidedAt.Add(RedemptionWindow)
	}
	if status == StatusExpired {
		return a.ExpiresAt
	}
	return a.ExpiresAt.Add(RedemptionWindow)
}

// ProposedChange answers the payload a redemption of this agent's own proposal
// would carry.
//
// It is read live, and that is the whole reason this method exists rather than
// the caller keeping a copy. A human may edit a staged proposal before
// releasing it (ADR-0036 §4), which rewrites both proposed_change and
// diff_hash — the original hash then opens nothing. An executor replaying what
// was originally staged would therefore try to perform what the agent asked for
// rather than what the person allowed, and would be refused for the mismatch.
func (s *Service) ProposedChange(ctx context.Context, id ids.ApprovalID) (json.RawMessage, error) {
	var change json.RawMessage
	if err := s.readOwnProposal(ctx, id, func(a row) { change = a.ProposedChange }); err != nil {
		return nil, err
	}
	return change, nil
}

// Withdraw retracts this agent's own live proposal on its own transaction — the
// wrapper WithdrawInTx has lacked, for callers that hold none.
//
// retracted reports whether there was still an offer to take. It is FALSE for
// an approval a human already decided — what a person answered is not the
// agent's to take back — and a caller that reported "withdrawn" either way
// would tell its user the proposal was gone while it sat decided in the inbox.
func (s *Service) Withdraw(ctx context.Context, id ids.ApprovalID, reason string) (retracted bool, err error) {
	passport, err := stagingPassport(ctx)
	if err != nil {
		return false, err
	}
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := ownProposal(ctx, tx, id, passport); err != nil {
			return err
		}
		var wErr error
		retracted, wErr = s.WithdrawInTx(ctx, tx, id, reason)
		return wErr
	})
	if err != nil {
		return false, fmt.Errorf("crmapprovals: withdrawing approval: %w", err)
	}
	return retracted, nil
}

// readOwnProposal runs read over the calling passport's own staged proposal.
func (s *Service) readOwnProposal(ctx context.Context, id ids.ApprovalID, read func(row)) error {
	passport, err := stagingPassport(ctx)
	if err != nil {
		return err
	}
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := ownProposal(ctx, tx, id, passport)
		if err != nil {
			return err
		}
		read(a)
		return nil
	})
	if err != nil {
		return fmt.Errorf("crmapprovals: reading a staged proposal: %w", err)
	}
	return nil
}

// ownProposal fetches an approval only if THIS passport staged it.
//
// The nil check is not belt-and-braces: passport_id is nullable, and a proposal
// staged by a human carries none. Comparing a non-nil caller against a nil
// column has to answer "not yours" rather than dereferencing it.
func ownProposal(ctx context.Context, tx pgx.Tx, id ids.ApprovalID, passport ids.PassportID) (row, error) {
	a, err := get(ctx, tx, id)
	if err != nil {
		return row{}, err
	}
	if a.PassportID == nil || *a.PassportID != passport {
		return row{}, apperrors.ErrNotFound
	}
	return a, nil
}

// stagingPassport answers the agent passport a poll is acting under.
//
// A ZERO id is refused as hard as a missing principal. principal.Actor answers
// ok for a zero-value Principal, and a zero passport would compare equal to
// nothing and therefore reach the not-found answer anyway — but by accident.
// Saying so here makes the refusal the rule rather than a consequence.
func stagingPassport(ctx context.Context) (ids.PassportID, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.PassportID.IsZero() {
		return ids.PassportID{}, errors.New("crmapprovals: only an agent passport may poll its own staged proposal")
	}
	return ids.From[ids.PassportKind](actor.PassportID), nil
}
