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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/settings"
	portsettings "github.com/gradionhq/margince/backend/internal/shared/ports/settings"
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

// baseCurrency reads the installation's reporting currency.
func (s *Store) baseCurrency(ctx context.Context, tx pgx.Tx) (string, error) {
	if s.settings == nil {
		return "", errors.New("finance: the mirror needs the installation-settings seam; " +
			"construct the store through compose, or call WithSettings before mirroring")
	}
	base, err := settings.GetTx(ctx, tx, s.settings, portsettings.InstallationBaseCurrency)
	if err != nil {
		return "", fmt.Errorf("read the installation's base currency: %w", err)
	}
	return base, nil
}
