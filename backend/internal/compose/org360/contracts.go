// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// What the account is under contract for (ADR-0109/A160).
//
// Two sums, never one. A three-year total and a per-year figure describe
// different spans, so adding them produces a number that means nothing — the
// card shows both, labelled, or neither.

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/modules/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// contractStrip is what the state strip says about an account's agreements.
// Every figure is null-not-zero: a reader who may not see contracts, and an
// account with none, are different facts and only one is about the account.
type contractStrip struct {
	activeCount int
	// totalBasisMinorBase and annualizedMinorBase are kept apart on purpose
	// (ADR-0109 §5) and are only set when something priced contributed.
	totalBasisMinorBase *int64
	annualizedMinorBase *int64
	pricedCount         int
	baseCurrency        string
	nearestRenewalOn    *time.Time
	cancellationPending bool
	cancellationOn      *time.Time
}

// readContractStrip sums the account's active agreements by basis.
//
// The "active" test is the DERIVED reading (CONTRACT-FORM-1), not the status
// column: a contract whose dates have passed while its status change waits for
// approval is not under contract, and counting it would let an approval queue
// render as a live customer.
//
// Conversion is one multiply per contract at its own frozen rate, before the
// sum, so the headline reconciles with the rows beneath it.
func readContractStrip(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID,
	asOf time.Time, baseCcy string,
) (contractStrip, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	// The as-of is the DATE, not the instant. The columns are dates, and an
	// aggregate evaluated at a timestamp drops a contract ending today the
	// moment midnight passes while the contract's own read still calls it
	// active all day — two surfaces disagreeing about the same row.
	asOfPos := arg(asOf.UTC().Truncate(24 * time.Hour))

	// The SAME predicate the contracts module applies to a single read. An
	// aggregate that skipped it would leak through its total: a reader who
	// cannot open a colleague's deal would still learn what its agreement is
	// worth from the account headline.
	scope, err := contracts.VisibleClause(ctx, "", arg)
	if err != nil {
		return contractStrip{}, err
	}
	where := storekit.SQLf(`organization_id = $%d
		  AND archived_at IS NULL
		  AND status NOT IN ('draft', 'superseded')
		  AND (starts_on IS NULL OR starts_on <= $%d)
		  AND (LEAST(ends_on, cancellation_effective_on) IS NULL
		       OR $%d <= LEAST(ends_on, cancellation_effective_on))`, orgPos, asOfPos, asOfPos)
	if scope != "" {
		where += " AND " + scope
	}

	rows, err := tx.Query(ctx, storekit.SQLf(`
		SELECT value_basis, value_minor, fx_rate_to_base, currency,
		       renewal_on::timestamptz, cancellation_effective_on::timestamptz
		FROM contract WHERE %s`, where), args...)
	if err != nil {
		return contractStrip{}, fmt.Errorf("read the account's active contracts: %w", err)
	}
	defer rows.Close()

	strip := contractStrip{baseCurrency: baseCcy}
	var totalBasis, annualized int64
	var haveTotal, haveAnnualized bool

	for rows.Next() {
		var (
			basis      string
			valueMinor *int64
			rate       *pgtype.Numeric
			currency   *string
			renewalOn  *time.Time
			cancelOn   *time.Time
		)
		if err := rows.Scan(&basis, &valueMinor, &rate, &currency, &renewalOn, &cancelOn); err != nil {
			return contractStrip{}, fmt.Errorf("scan an active contract: %w", err)
		}
		strip.activeCount++

		if renewalOn != nil && (strip.nearestRenewalOn == nil || renewalOn.Before(*strip.nearestRenewalOn)) {
			strip.nearestRenewalOn = renewalOn
		}
		// Notice recorded and the end date still ahead: the customer IS under
		// contract, and the card says "ends on" rather than reading as though
		// they had already gone.
		if cancelOn != nil {
			strip.cancellationPending = true
			if strip.cancellationOn == nil || cancelOn.Before(*strip.cancellationOn) {
				strip.cancellationOn = cancelOn
			}
		}

		converted, ok := contractValueInBase(valueMinor, currency, rate, baseCcy)
		if !ok {
			continue
		}
		strip.pricedCount++
		if basis == "annualized_12m" {
			annualized += converted
			haveAnnualized = true
			continue
		}
		totalBasis += converted
		haveTotal = true
	}
	if err := rows.Err(); err != nil {
		return contractStrip{}, fmt.Errorf("read the account's contract page: %w", err)
	}

	if haveTotal {
		strip.totalBasisMinorBase = &totalBasis
	}
	if haveAnnualized {
		strip.annualizedMinorBase = &annualized
	}
	return strip, nil
}

// contractValueInBase converts one agreement's value into the base currency,
// reporting whether it could be converted at all.
//
// An agreement already in the base currency needs no rate and contributes as
// it stands. One in another currency contributes only with its own frozen
// rate: converting at today's rate would restate history every time somebody
// opened the page, and leaving it out of the sum is what `priced_count` exists
// to disclose.
//
// ROUNDS, never truncates, and does its arithmetic in the same exact decimal
// the database holds the rate in. Truncating turns one minor unit at a half
// rate into nothing, and a float64 multiply loses whole units above 2^53 —
// both are silent, and both would make a headline disagree with the rows a
// reader can add up themselves. This is the spelling the deal conversion uses.
func contractValueInBase(valueMinor *int64, currency *string, rate *pgtype.Numeric, baseCcy string) (int64, bool) {
	if valueMinor == nil || currency == nil {
		return 0, false
	}
	if *currency == baseCcy {
		return *valueMinor, true
	}
	if rate == nil || !rate.Valid {
		return 0, false
	}
	exact := new(big.Rat).SetInt(big.NewInt(*valueMinor))
	exact.Mul(exact, numericAsRat(rate))
	return roundRat(exact), true
}

// numericAsRat reads a Postgres numeric as an exact rational, so no step of the
// conversion passes through a float.
func numericAsRat(n *pgtype.Numeric) *big.Rat {
	out := new(big.Rat).SetInt(n.Int)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs32(n.Exp))), nil)
	if n.Exp < 0 {
		return out.Quo(out, new(big.Rat).SetInt(scale))
	}
	return out.Mul(out, new(big.Rat).SetInt(scale))
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// roundRat rounds half away from zero, which is what the database's
// round(numeric) does — the two must agree or a converted figure changes
// depending on which side computed it.
func roundRat(r *big.Rat) int64 {
	half := big.NewRat(1, 2)
	adjusted := new(big.Rat).Set(r)
	if r.Sign() < 0 {
		adjusted.Sub(adjusted, half)
	} else {
		adjusted.Add(adjusted, half)
	}
	return new(big.Int).Quo(adjusted.Num(), adjusted.Denom()).Int64()
}

// fillContractStrip renders the read onto the wire block.
//
// The two sums stay apart and each is set only when something contributed, so
// a reader never meets a zero that would claim agreements worth nothing. The
// currency travels with the figures rather than being looked up beside them: a
// converted total rendered under a currency fetched from somewhere else is the
// unlabelled cross-currency sum the page rules forbid.
func fillContractStrip(out *struct {
	ActiveCount              int                 `json:"active_count"`
	AnnualizedValueMinorBase *int                `json:"annualized_value_minor_base,omitempty"`
	BaseCurrency             *string             `json:"base_currency,omitempty"`
	CancellationEffectiveOn  *openapi_types.Date `json:"cancellation_effective_on,omitempty"`
	CancellationPending      bool                `json:"cancellation_pending"`
	NearestRenewalOn         *openapi_types.Date `json:"nearest_renewal_on,omitempty"`
	PricedCount              *int                `json:"priced_count,omitempty"`
	TotalBasisValueMinorBase *int                `json:"total_basis_value_minor_base,omitempty"`
}, read contractStrip,
) {
	out.ActiveCount = read.activeCount
	out.CancellationPending = read.cancellationPending
	priced := read.pricedCount
	out.PricedCount = &priced

	if read.totalBasisMinorBase != nil {
		total := int(*read.totalBasisMinorBase)
		out.TotalBasisValueMinorBase = &total
	}
	if read.annualizedMinorBase != nil {
		annual := int(*read.annualizedMinorBase)
		out.AnnualizedValueMinorBase = &annual
	}
	if out.TotalBasisValueMinorBase != nil || out.AnnualizedValueMinorBase != nil {
		currency := read.baseCurrency
		out.BaseCurrency = &currency
	}
	if read.nearestRenewalOn != nil {
		out.NearestRenewalOn = &openapi_types.Date{Time: *read.nearestRenewalOn}
	}
	if read.cancellationOn != nil {
		out.CancellationEffectiveOn = &openapi_types.Date{Time: *read.cancellationOn}
	}
}
