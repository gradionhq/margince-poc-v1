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
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

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
	rows, err := tx.Query(ctx, `
		SELECT value_basis,
		       value_minor,
		       fx_rate_to_base,
		       currency,
		       renewal_on::timestamptz,
		       cancellation_effective_on::timestamptz
		FROM contract
		WHERE organization_id = $1
		  AND archived_at IS NULL
		  AND status NOT IN ('draft', 'superseded')
		  AND (starts_on IS NULL OR starts_on <= $2)
		  AND (LEAST(ends_on, cancellation_effective_on) IS NULL
		       OR $2 <= LEAST(ends_on, cancellation_effective_on))`,
		orgID, asOf)
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
			rate       *float64
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
func contractValueInBase(valueMinor *int64, currency *string, rate *float64, baseCcy string) (int64, bool) {
	if valueMinor == nil || currency == nil {
		return 0, false
	}
	if *currency == baseCcy {
		return *valueMinor, true
	}
	if rate == nil {
		return 0, false
	}
	return int64(float64(*valueMinor) * *rate), true
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
