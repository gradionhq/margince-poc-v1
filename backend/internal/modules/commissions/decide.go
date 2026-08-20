// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package commissions

// The ledger's decisions: approve, pay, void.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The three decisions a human makes about an entry.
const (
	DecisionApprove = "approve"
	DecisionPay     = "pay"
	DecisionVoid    = "void"
)

// legalTransitions is the ledger's lifecycle, stated once. A decision absent
// from the map for the entry's current status is refused — the alternative is
// paying an entry nobody approved, or approving one already voided.
var legalTransitions = map[string]map[string]string{
	DecisionApprove: {StatusAccrued: StatusApproved},
	DecisionPay:     {StatusApproved: StatusPaid},
	// Void reaches every live state. An accrual can be cancelled before anyone
	// looks at it, and a payment already made is still reversed here — the
	// reversal row is the record that money went out and came back.
	DecisionVoid: {
		StatusAccrued:  StatusVoid,
		StatusApproved: StatusVoid,
		StatusPaid:     StatusVoid,
	},
}

// DecideInput is one decision about one entry.
type DecideInput struct {
	Decision string
	// Reason is required for a void. An entry cancelled without a stated
	// reason cannot be explained to the partner who was told they earned it.
	Reason    *string
	IfVersion *int64
}

// Decide moves one entry through the ledger's lifecycle.
//
// A void writes TWO rows: the original goes void, and a reversal is inserted
// beside it carrying the negated arrangement. The pair is what makes a clawback
// auditable — the original still says what was earned, and the reversal says it
// was taken back and why.
func (s *Store) Decide(ctx context.Context, id ids.CommissionEntryID, in DecideInput) (crmcontracts.CommissionEntry, error) {
	if err := auth.Require(ctx, commissionObject, principal.ActionUpdate); err != nil {
		return crmcontracts.CommissionEntry{}, err
	}
	if in.Decision == DecisionVoid && emptyReason(in.Reason) {
		return crmcontracts.CommissionEntry{}, &VoidNeedsReasonError{}
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.CommissionEntry{}, err
	}

	var out crmcontracts.CommissionEntry
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = decideTx(ctx, tx, id, in, by)
		return err
	})
	return out, err
}

func decideTx(ctx context.Context, tx pgx.Tx, id ids.CommissionEntryID, in DecideInput, by string) (crmcontracts.CommissionEntry, error) {
	// Read under the caller's scope first: deciding an entry is a write to a
	// row they must already be able to see, and a miss reads as not-found.
	current, err := readEntry(ctx, tx, id)
	if err != nil {
		return crmcontracts.CommissionEntry{}, err
	}

	allowed, known := legalTransitions[in.Decision]
	if !known {
		return crmcontracts.CommissionEntry{}, &UnknownDecisionError{Got: in.Decision}
	}
	next, legal := allowed[string(current.Status)]
	if !legal {
		return crmcontracts.CommissionEntry{}, &IllegalTransitionError{
			Decision: in.Decision, From: string(current.Status),
		}
	}

	p := storekit.NewPatch()
	p.Set("status", current.Status, next)
	if in.Decision == DecisionVoid {
		p.Set("void_reason", current.VoidReason, *in.Reason)
	}
	if err := p.ApplyGuarded(ctx, tx, "commission_entry", ids.UUID(current.Id), in.IfVersion); err != nil {
		return crmcontracts.CommissionEntry{}, fmt.Errorf("apply commission decision: %w", err)
	}
	if in.Decision == DecisionVoid {
		if err := insertReversal(ctx, tx, current, *in.Reason, by); err != nil {
			return crmcontracts.CommissionEntry{}, err
		}
	}

	auditID, err := storekit.Audit(ctx, tx, in.Decision, commissionObject, ids.UUID(current.Id),
		p.Before(), p.After())
	if err != nil {
		return crmcontracts.CommissionEntry{}, fmt.Errorf("audit commission decision: %w", err)
	}
	decided := crmcontracts.PublicEventCommissionDecided{
		Decision: in.Decision, FromStatus: string(current.Status), ToStatus: next,
	}
	if in.Decision == DecisionVoid {
		decided.Reason = in.Reason
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, ids.UUID(current.Id), decided); err != nil {
		return crmcontracts.CommissionEntry{}, fmt.Errorf("emit commission.decided: %w", err)
	}
	return readEntry(ctx, tx, id)
}

// insertReversal writes the row that cancels an entry. It is born void and
// carries the same arrangement as the entry it undoes, so a reader sees what
// was taken back rather than having to reconstruct it from the original.
func insertReversal(ctx context.Context, tx pgx.Tx, original crmcontracts.CommissionEntry, reason, by string) error {
	id := ids.New[ids.CommissionEntryKind]()
	_, err := tx.Exec(ctx,
		`INSERT INTO commission_entry (id, deal_id, partner_org_id, status,
		                               attribution_at_accrual, margin_tier_at_accrual, rate_bps,
		                               basis_amount_minor, currency, fx_rate_to_base, amount_minor,
		                               reversal_of, void_reason, captured_by)
		 VALUES ($1, $2, $3, 'void', $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		id, ids.UUID(original.DealId), ids.UUID(original.PartnerOrgId),
		original.AttributionAtAccrual, original.MarginTierAtAccrual, original.RateBps,
		original.BasisAmountMinor, original.Currency, original.FxRateToBase, original.AmountMinor,
		ids.UUID(original.Id), reason, by)
	if err != nil {
		return fmt.Errorf("insert commission reversal: %w", err)
	}
	return nil
}

func emptyReason(reason *string) bool {
	return reason == nil || *reason == ""
}

// VoidNeedsReasonError maps to 422.
type VoidNeedsReasonError struct{}

func (e *VoidNeedsReasonError) Error() string {
	return "voiding a commission entry needs a reason: the partner was told they earned this"
}

// FieldFault refuses a void with no stated reason.
func (e *VoidNeedsReasonError) FieldFault() (field, code, message string) {
	return "reason", "commission_void_needs_reason", e.Error()
}

// UnknownDecisionError maps to 422: the vocabulary is closed.
type UnknownDecisionError struct{ Got string }

func (e *UnknownDecisionError) Error() string {
	return "decision must be " + DecisionApprove + ", " + DecisionPay + " or " + DecisionVoid
}

// FieldFault refuses a decision outside the three-word vocabulary.
func (e *UnknownDecisionError) FieldFault() (field, code, message string) {
	return "decision", "commission_decision_invalid", e.Error()
}

// IllegalTransitionError maps to 422: the ledger's lifecycle is ordered, and
// naming where the entry actually is tells the caller what to do instead.
type IllegalTransitionError struct{ Decision, From string }

func (e *IllegalTransitionError) Error() string {
	return "cannot " + e.Decision + " an entry that is " + e.From
}

// FieldFault refuses a decision the entry's current state does not admit.
func (e *IllegalTransitionError) FieldFault() (field, code, message string) {
	return "decision", "commission_transition_illegal", e.Error()
}

// ReverseForDeal voids every live entry a deal produced, and returns how many
// it voided.
//
// The reopen path. It takes no version: the caller is the accrual consumer
// reacting to a win being undone, not a human holding a stale copy of the row,
// and refusing on version skew would leave money recorded against a deal that
// is no longer won. Entries already void are skipped rather than reversed
// twice, which is what makes a redelivered reopen harmless.
func (s *Store) ReverseForDeal(ctx context.Context, deal ids.DealID, reason string) (int, error) {
	if err := auth.Require(ctx, commissionObject, principal.ActionUpdate); err != nil {
		return 0, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return 0, err
	}

	var reversed int
	err = s.tx(ctx, func(tx pgx.Tx) error {
		live, err := liveEntriesForDeal(ctx, tx, deal)
		if err != nil {
			return err
		}
		for _, entry := range live {
			if err := voidOne(ctx, tx, entry, reason, by); err != nil {
				return err
			}
			reversed++
		}
		return nil
	})
	return reversed, err
}

// liveEntriesForDeal reads the entries a reversal would act on, under the
// caller's own scope — a reversal is a write to rows it must be able to see.
func liveEntriesForDeal(ctx context.Context, tx pgx.Tx, deal ids.DealID) ([]crmcontracts.CommissionEntry, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	dealPos := arg(deal)

	where := storekit.SQLf("deal_id = $%d AND status <> 'void' AND reversal_of IS NULL", dealPos)
	scope, err := VisibleClause(ctx, "", arg)
	if err != nil {
		return nil, err
	}
	if scope != "" {
		where += " AND " + scope
	}

	rows, err := tx.Query(ctx,
		`SELECT `+commissionColumns+` FROM commission_entry WHERE `+where+` FOR UPDATE`, args...)
	if err != nil {
		return nil, fmt.Errorf("read live commission entries: %w", err)
	}
	defer rows.Close()

	var live []crmcontracts.CommissionEntry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan live commission entry: %w", err)
		}
		live = append(live, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read live commission entries: %w", err)
	}
	return live, nil
}

// voidOne marks one entry void and writes the reversal beside it — the same
// pair a human's void produces, so a reopened deal and a cancelled entry leave
// the ledger in the same shape.
func voidOne(ctx context.Context, tx pgx.Tx, entry crmcontracts.CommissionEntry, reason, by string) error {
	lock, err := storekit.LockRow(ctx, tx, "commission_entry", ids.UUID(entry.Id), storekit.LiveOnly)
	if err != nil {
		return fmt.Errorf("lock commission entry: %w", err)
	}
	p := storekit.NewPatch()
	p.Set("status", entry.Status, StatusVoid)
	p.Set("void_reason", entry.VoidReason, reason)
	if err := p.ApplyLocked(ctx, tx, lock); err != nil {
		return fmt.Errorf("void commission entry: %w", err)
	}
	if err := insertReversal(ctx, tx, entry, reason, by); err != nil {
		return err
	}
	auditID, auditErr := storekit.Audit(ctx, tx, DecisionVoid, commissionObject, ids.UUID(entry.Id),
		p.Before(), p.After())
	if auditErr != nil {
		return fmt.Errorf("audit commission reversal: %w", auditErr)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, ids.UUID(entry.Id), crmcontracts.PublicEventCommissionDecided{
		Decision: DecisionVoid, FromStatus: string(entry.Status), ToStatus: StatusVoid, Reason: &reason,
	}); err != nil {
		return fmt.Errorf("emit commission.decided: %w", err)
	}
	return nil
}
