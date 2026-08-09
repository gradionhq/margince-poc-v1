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
	"time"

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
func (s *Service) decideBundleInTx(ctx context.Context, tx pgx.Tx, p principal.Principal, bundleID ids.UUID, approve bool, reason *string) ([]BundleMember, error) {
	rows, err := bundleMembers(ctx, tx, bundleID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]BundleMember, 0, len(rows))
	for _, a := range rows {
		// The probe classifies; decideInTx's own copy of it still guards the
		// write. Both are wanted: without this one an undecidable member would
		// abort the whole bundle instead of being invisible, and without that
		// one the decision would have an ungated entry point.
		visible, err := decidable(ctx, tx, p, a)
		if err != nil {
			return nil, err
		}
		if !visible {
			continue
		}
		if status := a.effectiveStatus(now); status != statusPending {
			out = append(out, BundleMember{Approval: a, Outcome: outcomeOf(status)})
			continue
		}
		member, err := s.decideMemberInTx(ctx, tx, p, a, approve, reason, now)
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

// decideMemberInTx decides ONE member through the same path a single decision
// takes, and absorbs the one race that path reports as an error: decideInTx
// takes the row lock, so a concurrent decision of that member commits first and
// this call finds it already decided. That is this member's outcome, not the
// bundle's failure, so the row is re-read for the verdict that won.
func (s *Service) decideMemberInTx(ctx context.Context, tx pgx.Tx, p principal.Principal, a row, approve bool, reason *string, now time.Time) (BundleMember, error) {
	decided, err := s.decideInTx(ctx, tx, p, a.ID, approve, reason, nil)
	if err == nil {
		return BundleMember{Approval: decided, Outcome: BundleDecided}, nil
	}
	var already *AlreadyDecidedError
	if !errors.As(err, &already) {
		return BundleMember{}, err
	}
	// AlreadyDecidedError is raised before any statement failed, so the
	// transaction is intact and can still read the row that won.
	fresh, getErr := get(ctx, tx, a.ID)
	if getErr != nil {
		return BundleMember{}, getErr
	}
	return BundleMember{Approval: fresh, Outcome: outcomeOf(fresh.effectiveStatus(now))}, nil
}

// outcomeOf maps a member's non-pending status onto what this call did to it —
// which is nothing. Expiry is its own outcome because it is not a decision
// anybody made: an expired proposal is re-proposed, never approved.
func outcomeOf(status string) BundleOutcome {
	if status == "expired" {
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
// them, which is also a stable lock order for two callers deciding the same
// bundle at once.
//
// It reads one row past the cap so a bundle too large to decide is a fact rather
// than a truncation nobody is told about.
func bundleMembers(ctx context.Context, tx pgx.Tx, bundleID ids.UUID) ([]row, error) {
	rows, err := collect(ctx, tx, `SELECT `+columns+` FROM approval
		WHERE bundle_id = $1 ORDER BY created_at, id LIMIT $2`, []any{bundleID, bundleDecisionCap + 1})
	if err != nil {
		return nil, err
	}
	if len(rows) > bundleDecisionCap {
		return nil, &BundleTooLargeError{Cap: bundleDecisionCap}
	}
	return rows, nil
}
