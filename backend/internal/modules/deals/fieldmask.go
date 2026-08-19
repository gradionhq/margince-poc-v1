// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The deal's field masks, applied where the row meets the wire. A rep reads
// every deal in the workspace; the amount of one that is not theirs to change
// is withheld — null, and named in masked_fields so the reader can tell it
// from an amount nobody entered.

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// dealMaskableFields are the columns a mask may name on a deal, and how each
// is withheld. A mask naming a column not listed here is inert: withholding
// is a deliberate act per field, not a reflective one over the struct.
var dealMaskableFields = map[string]func(*crmcontracts.Deal){
	// The money pair goes together: a currency beside a withheld amount
	// would read as a priced deal with its figure missing.
	"amount_minor": func(d *crmcontracts.Deal) { d.AmountMinor, d.Currency = nil, nil },
	"currency":     func(d *crmcontracts.Deal) { d.Currency = nil },
}

// maskDeals withholds, per row, the fields the caller's role masks. One
// statement answers which rows of the page the caller could change; the
// masks conditioned on write authority lift on those.
func maskDeals(ctx context.Context, tx pgx.Tx, deals []crmcontracts.Deal) error {
	p, err := storekit.Actor(ctx)
	if err != nil {
		return err
	}
	// Cheap exit for the common case — no mask on deals at all.
	if len(auth.MaskedFields(p, "deal", false)) == 0 {
		return nil
	}
	rowIDs := make([]ids.UUID, 0, len(deals))
	for _, d := range deals {
		rowIDs = append(rowIDs, ids.UUID(d.Id))
	}
	writable, err := auth.WritableSubset(ctx, tx, "deal", rowIDs)
	if err != nil {
		return err
	}
	for i := range deals {
		deal := &deals[i]
		masked := auth.MaskedFields(p, "deal", writable[ids.UUID(deal.Id)])
		if len(masked) == 0 {
			continue
		}
		named := make([]string, 0, len(masked))
		for _, field := range masked {
			withhold, known := dealMaskableFields[field]
			if !known {
				continue
			}
			withhold(deal)
			named = append(named, field)
		}
		if len(named) > 0 {
			deal.MaskedFields = &named
		}
	}
	return nil
}

// refuseMaskedSort refuses a sort over a column the caller's role masks on
// any row: ordering by a value is reading it, and a page ordered by amounts
// the caller may not see would disclose them through the order.
func refuseMaskedSort(ctx context.Context, sort *string) error {
	if sort == nil || *sort == "" {
		return nil
	}
	field := strings.TrimPrefix(strings.TrimSpace(*sort), "-")
	if _, maskable := dealMaskableFields[field]; !maskable {
		return nil
	}
	masked, err := auth.MasksAnyRowOf(ctx, "deal", field)
	if err != nil {
		return err
	}
	if masked {
		return &values.ParseError{
			Field: "sort", Code: "field_masked",
			Message: "sort by " + field + " is not available: your role does not read it on every deal",
		}
	}
	return nil
}
