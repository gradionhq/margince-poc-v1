// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package commissions

// Accrual: turning a won deal into what its partner earned.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AccrueInput is one won deal, as the transition that won it described it.
//
// Every value here is a SNAPSHOT the caller read at won time rather than a
// reference the store resolves: the accrual must record the arrangement as it
// stood that day, and a deal reopened later clears its frozen FX, so a
// read-back would answer a different question than the one being paid on.
type AccrueInput struct {
	DealID       ids.DealID
	PartnerOrgID ids.OrganizationID
	// TriggerEventID is the transition that produced this accrual. Stored
	// unique, so a replayed event fails instead of paying twice; nil for an
	// entry a human is creating by hand.
	TriggerEventID *ids.UUID
	Attribution    string
	MarginTier     *string
	RateBps        int
	BasisMinor     int64
	Currency       string
	FxRateToBase   *string
}

// ErrNotAccruable reports a won deal that earns nothing — the ordinary case,
// not a failure. A deal with no partner, an influenced one, or a partner with
// no tier set all reach here, and the caller records the skip rather than
// retrying it forever.
var ErrNotAccruable = errors.New("commission: this deal accrues nothing")

// ErrAlreadyAccrued reports that this transition already produced an entry.
// A replay, not a defect: the unique trigger_event_id did its job.
var ErrAlreadyAccrued = errors.New("commission: this transition already accrued")

// Accrue records what a partner earned on a won deal.
//
// It is idempotent on TriggerEventID by construction rather than by checking
// first: the unique index is the arbiter, so two concurrent deliveries of the
// same event cannot both pass a pre-check and both insert.
func (s *Store) Accrue(ctx context.Context, in AccrueInput) (crmcontracts.CommissionEntry, error) {
	if err := auth.Require(ctx, commissionObject, principal.ActionCreate); err != nil {
		return crmcontracts.CommissionEntry{}, err
	}
	if in.Attribution != AttributionSourced {
		// Only a partner who BROUGHT the deal earns on it. An influenced deal
		// is reporting, not money.
		return crmcontracts.CommissionEntry{}, ErrNotAccruable
	}
	if in.RateBps <= 0 {
		// A tier that resolves to nothing is a partner whose arrangement was
		// never set. Accruing zero would state, durably, that they earned
		// nothing on a deal they brought.
		return crmcontracts.CommissionEntry{}, ErrNotAccruable
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.CommissionEntry{}, err
	}

	var out crmcontracts.CommissionEntry
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = accrueTx(ctx, tx, in, by)
		return err
	})
	return out, err
}

func accrueTx(ctx context.Context, tx pgx.Tx, in AccrueInput, by string) (crmcontracts.CommissionEntry, error) {
	// The deal and the partner are both row-scoped records, and an entry that
	// pointed at either one the caller cannot see would be unreadable the
	// moment it was written.
	if err := auth.EnsureLinkTarget(ctx, tx, "deal", in.DealID.UUID); err != nil {
		return crmcontracts.CommissionEntry{}, err
	}
	if err := auth.EnsureLinkTarget(ctx, tx, "organization", in.PartnerOrgID.UUID); err != nil {
		return crmcontracts.CommissionEntry{}, err
	}

	id := ids.New[ids.CommissionEntryKind]()
	amount := commissionAmount(in.BasisMinor, in.RateBps)
	_, err := tx.Exec(ctx,
		`INSERT INTO commission_entry (id, deal_id, partner_org_id, trigger_event_id,
		                               attribution_at_accrual, margin_tier_at_accrual, rate_bps,
		                               basis_amount_minor, currency, fx_rate_to_base, amount_minor,
		                               captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		id, in.DealID, in.PartnerOrgID, in.TriggerEventID,
		in.Attribution, in.MarginTier, in.RateBps,
		in.BasisMinor, in.Currency, in.FxRateToBase, amount, by)
	if err != nil {
		if storekit.IsUniqueViolation(err) {
			// Either this event was delivered twice, or the deal already has a
			// live entry. Both mean "already accrued", and neither is a fault.
			return crmcontracts.CommissionEntry{}, ErrAlreadyAccrued
		}
		return crmcontracts.CommissionEntry{}, fmt.Errorf("insert commission entry: %w", err)
	}

	auditID, err := storekit.Audit(ctx, tx, "accrue", commissionObject, id.UUID, nil,
		map[string]any{
			"deal_id": in.DealID.UUID, "partner_org_id": in.PartnerOrgID.UUID,
			"rate_bps": in.RateBps, "amount_minor": amount, "currency": in.Currency,
		})
	if err != nil {
		return crmcontracts.CommissionEntry{}, fmt.Errorf("audit commission accrual: %w", err)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventCommissionAccrued{
		DealId:       openapi_types.UUID(in.DealID.UUID),
		PartnerOrgId: openapi_types.UUID(in.PartnerOrgID.UUID),
		AmountMinor:  amount,
		Currency:     in.Currency,
		RateBps:      in.RateBps,
	}); err != nil {
		return crmcontracts.CommissionEntry{}, fmt.Errorf("emit commission.accrued: %w", err)
	}
	return readEntry(ctx, tx, id)
}

// commissionAmount applies a basis-point rate to a minor-unit amount.
//
// Integer arithmetic throughout, dividing last, so the result is exact minor
// units rather than a float rounded on the way through. Truncation is toward
// zero, which under-pays by at most one minor unit — the direction that never
// over-states what is owed.
func commissionAmount(basisMinor int64, rateBps int) int64 {
	// money-scale-exempt: 10_000 is the BASIS-POINT denominator, not a minor
	// unit. basisMinor arrives in minor units and stays in them; the division
	// converts a rate, and routing it through the ISO table would be a category
	// error. Named here rather than excluded in the gate, so a reader of this
	// line meets the reason.
	return basisMinor * int64(rateBps) / 10_000 // money-scale-exempt: basis points, see above
}

// RateBpsForTier maps a partner's margin tier onto the rate it means.
//
// The tier vocabulary encodes its own percentage (tier1_15 is 15%), so this
// reads the number out of the name rather than keeping a second table that
// could disagree with the first. An unknown tier answers 0, which Accrue
// treats as "no arrangement" rather than "free".
func RateBpsForTier(tier *string) int {
	if tier == nil {
		return 0
	}
	switch *tier {
	case "tier1_15":
		return 1500
	case "tier2_20":
		return 2000
	case "tier3_25":
		return 2500
	default:
		return 0
	}
}
