// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package finance

// What rate a mirrored invoice converts at, and the one case this build can
// answer without a rate sheet.
//
// There is no rate feed wired in. The formulas are base-currency by contract
// (FIN-PARAM-7/DM-FX-4) and refuse a total the moment one row cannot be
// converted (FIN-AC-6), so the honest answer for a foreign invoice is "no
// rate" — not 1, which would silently sum dollars into euros.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// The rate and the date it froze on are one fact (FIN-PARAM-7), so they are
// returned together: the identity's date is the invoice's own issue date, which
// is the day it was already in base currency.
func fxRateToBase(inv SourceInvoice, base string) (*float64, *time.Time) {
	if base == "" || !strings.EqualFold(inv.Currency, base) {
		return nil, nil
	}
	identity := 1.0
	issued := inv.IssuedOn
	return &identity, &issued
}

// baseCurrency reads the workspace's reporting currency.
func baseCurrency(ctx context.Context, tx pgx.Tx) (string, error) {
	var base string
	if err := tx.QueryRow(ctx,
		`SELECT base_currency FROM workspace WHERE id = $1`,
		storekit.MustWorkspace(ctx)).Scan(&base); err != nil {
		return "", fmt.Errorf("read the workspace's base currency: %w", err)
	}
	return base, nil
}
