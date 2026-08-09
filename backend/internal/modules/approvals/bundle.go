// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// One act's proposals, decided together. A bundle is a GROUPING over approval
// rows — `bundle_id`, no table, no entity, no lifecycle of its own (R7). It
// exists because one act routinely proposes several things at once (a site read
// stages the company's facts plus a lead per person it published) and the inbox
// otherwise shows them as unrelated questions.
//
// What it is NOT is a second authority object. ADR-0036 puts the authority in
// the staged row, and a bundle decision honours that: every member keeps its own
// diff hash, target version pin, expiry and verdict, and deciding the bundle
// records N per-effect decisions rather than one decision covering N effects.
// That is also why deciding is not all-or-nothing — the members were always
// independent, so one that expired or that someone else already decided reports
// for itself while its siblings decide normally.

package approvals

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// BundleOutcome names what one bundle decision did to one member.
type BundleOutcome string

const (
	// BundleDecided — the verdict was recorded by this call.
	BundleDecided BundleOutcome = "decided"
	// BundleAlreadyDecided — the member carried a verdict before this call,
	// which is left standing.
	BundleAlreadyDecided BundleOutcome = "already_decided"
	// BundleExpired — the member lapsed undecided and is no longer approvable.
	BundleExpired BundleOutcome = "expired"
	// BundleEffectFailed — the verdict IS recorded and audited, but the
	// follow-on change did not land.
	BundleEffectFailed BundleOutcome = "effect_failed"
)

// BundleMember is one member of a bundle and what the decision did to it.
type BundleMember struct {
	Approval row
	Outcome  BundleOutcome
}

// bundleDecisionCap bounds how many members one bundle decision covers. Bundles
// are minted by producers, never by a caller, and the largest today is a site
// read's company proposal plus a lead per published person — well inside this.
//
// Past the cap the decision is REFUSED rather than applied to a prefix: a
// partial decision reported as a whole one is the silent half-effect this file
// exists to prevent, and the members remain individually decidable.
const bundleDecisionCap = PendingScanCap

// BundleTooLargeError maps to 422: more members than one decision may cover.
type BundleTooLargeError struct{ Cap int }

func (e *BundleTooLargeError) Error() string {
	return fmt.Sprintf("this bundle holds more than %d proposals, which is more than one decision covers; decide its members individually", e.Cap)
}

// DecideBundle approves or rejects every still-pending member of one bundle.
//
// Authority is per member and unchanged: each is put through the same
// `decidable` probe the inbox and a single decision use, so a member this human
// could not decide alone is neither shown nor decided here — bundling is not a
// way to release an effect sideways. A bundle with no decidable member reads as
// absent (ErrNotFound), the same existence-hiding Get gives, so the bundle id
// cannot become a lookup oracle either.
func (s *Service) DecideBundle(ctx context.Context, bundleID ids.UUID, approve bool, reason *string) ([]BundleMember, error) {
	if err := humanOnly(ctx); err != nil {
		return nil, err
	}
	if bundleID.IsZero() {
		return nil, apperrors.ErrNotFound
	}
	p, _ := principal.Actor(ctx)
	var members []BundleMember
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		members, err = s.decideBundleInTx(ctx, tx, p, bundleID, approve, reason)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.releaseDecidedMembers(ctx, members, approve)
	return members, nil
}

// decideBundleInTx decides every decidable, still-pending member inside ONE
// transaction, so the bundle's verdicts land together or not at all. The
// follow-on effects deliberately do not: they run after the commit, exactly as
// a single decision's does.
//
// It reads and filters the whole bundle BEFORE deciding any of it, in that
// order for two reasons. A bundle nobody may decide has to read as absent
// whatever else is true of it — answering "too large" to a caller who cannot see
// a single member would confirm the act exists, which is the oracle this module
// closes everywhere else. And a bundle past the cap must be refused whole rather
// than applied to a prefix, which is only possible while nothing has been
// decided yet.
func (s *Service) decideBundleInTx(ctx context.Context, tx pgx.Tx, p principal.Principal, bundleID ids.UUID, approve bool, reason *string) ([]BundleMember, error) {
	rows, oversized, err := bundleMembers(ctx, tx, bundleID)
	if err != nil {
		return nil, err
	}
	mine, err := decidableMembers(ctx, tx, p, rows)
	if err != nil {
		return nil, err
	}
	if len(mine) == 0 {
		return nil, apperrors.ErrNotFound
	}
	if oversized {
		return nil, &BundleTooLargeError{Cap: bundleDecisionCap}
	}
	now := s.now()
	out := make([]BundleMember, 0, len(mine))
	for _, a := range mine {
		if status := a.effectiveStatus(now); status != statusPending {
			out = append(out, BundleMember{Approval: a, Outcome: outcomeOf(status)})
			continue
		}
		member, err := s.decideMemberInTx(ctx, tx, p, a, approve, reason)
		if err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	if len(out) == 0 {
		return nil, apperrors.ErrNotFound
	}
	return out, nil
}

// decidableMembers keeps the members this caller could decide one at a time, by
// the same predicate the inbox and a single decision use. A member outside their
// authority is invisible here exactly as it is there — never a refusal, which
// would confirm it exists.
func decidableMembers(ctx context.Context, tx pgx.Tx, p principal.Principal, rows []row) ([]row, error) {
	mine := make([]row, 0, len(rows))
	for _, a := range rows {
		visible, err := decidable(ctx, tx, p, a)
		if err != nil {
			return nil, err
		}
		if visible {
			mine = append(mine, a)
		}
	}
	return mine, nil
}

// decideMemberInTx decides ONE member through the same path a single decision
// takes, and absorbs the one answer that path gives about the MEMBER rather than
// about the bundle.
//
// decideInTx judges the row's status against the service clock at the moment it
// runs, while the loop above judged every member against the clock the decision
// opened with. A member sitting on its expiry boundary is pending to the one
// reading and lapsed to the other, and the later reading is the right one — but
// it arrives as an error, and letting it propagate would roll back every sibling
// verdict already written over a member nobody could have decided anyway.
//
// It is raised before any statement has failed, so the transaction is intact and
// the row can still be read for what it says now. The outcome comes from the
// status the REFUSAL named rather than from re-judging that fresh row against
// the older clock, which would see it as still pending and answer
// "already_decided" — telling a human somebody decided a question nobody did.
func (s *Service) decideMemberInTx(ctx context.Context, tx pgx.Tx, p principal.Principal, a row, approve bool, reason *string) (BundleMember, error) {
	decided, err := s.decideInTx(ctx, tx, p, a.ID, approve, reason, nil)
	if err == nil {
		return BundleMember{Approval: decided, Outcome: BundleDecided}, nil
	}
	var already *AlreadyDecidedError
	if !errors.As(err, &already) {
		return BundleMember{}, err
	}
	fresh, getErr := get(ctx, tx, a.ID)
	if getErr != nil {
		return BundleMember{}, getErr
	}
	return BundleMember{Approval: fresh, Outcome: outcomeOf(already.Status)}, nil
}

// outcomeOf maps a member's non-pending status onto what this call did to it —
// which is nothing. Expiry is its own outcome because it is not a decision
// anybody made: an expired proposal is re-proposed, never approved.
func outcomeOf(status string) BundleOutcome {
	if status == StatusExpired {
		return BundleExpired
	}
	return BundleAlreadyDecided
}

// releaseDecidedMembers runs each newly decided member's follow-on effect, after
// the decision transaction has committed.
//
// A failure is that member's outcome and no one else's. The decisions are
// committed, so there is nothing to roll back and nothing to retry: the member
// reads approved-and-unredeemed, its audit trail says how far it got, and the
// caller is told which one did not land. The cause goes to the log because the
// wire deliberately carries no internals to a client.
func (s *Service) releaseDecidedMembers(ctx context.Context, members []BundleMember, approve bool) {
	if !approve {
		return // a rejection releases nothing
	}
	for i := range members {
		if members[i].Outcome != BundleDecided {
			continue
		}
		a := members[i].Approval
		if err := s.runDecisionEffect(ctx, a.ID, a, true); err != nil {
			members[i].Outcome = BundleEffectFailed
			s.logger().ErrorContext(ctx, "bundle member approved but its effect failed",
				"approval", a.ID.String(), "kind", a.Kind, "err", err)
		}
	}
}

// bundleMembers reads one bundle's rows oldest-first — the order the act staged
// them, and a deterministic order, so two callers deciding the same bundle at
// once queue behind each other instead of deadlocking.
//
// The rows are LOCKED as they are read, which is what makes membership hold
// still for the rest of the decision. Without it, a re-proposal joining a member
// (rebundleJoinedInTx) can move that row onto a fresher act's bundle in the gap
// between this read and the write — and the decision would then answer the OLD
// bundle's question by deciding a row that had already become part of the new
// one, leaving the fresh act's bundle carrying a member nobody decided there.
// One statement, in one order, so the locks are also taken more safely than the
// per-member walk would take them.
//
// It reads one row past the cap and reports oversized rather than refusing here,
// so the caller can put the authority question first: a bundle whose existence
// this human may not learn of must read as absent whatever its size. The extra
// row is what makes "too large" a fact rather than a truncation nobody is told
// about.
func bundleMembers(ctx context.Context, tx pgx.Tx, bundleID ids.UUID) (rows []row, oversized bool, err error) {
	rows, err = collect(ctx, tx, `SELECT `+columns+` FROM approval
		WHERE bundle_id = $1 ORDER BY created_at, id LIMIT $2 FOR UPDATE`,
		[]any{bundleID, bundleDecisionCap + 1})
	if err != nil {
		return nil, false, err
	}
	return rows, len(rows) > bundleDecisionCap, nil
}
