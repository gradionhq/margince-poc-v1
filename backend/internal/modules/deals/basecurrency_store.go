// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The workspace base currency: the one code every rate in the sheet converts
// INTO, and the one an installation may still correct while no deal has frozen
// a conversion against it. It sits beside the rate sheet (fxrate_store.go)
// rather than inside it because it answers a different question — the sheet is
// priced against this value, and a reader asking whether it can still move
// should not have to read the rate-write path to find out.

package deals

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// baseCurrencyColumn is the workspace column the whole rate sheet converts to.
const baseCurrencyColumn = "base_currency"

// BaseCurrencyState is the workspace base currency together with whether it can
// still change. `Locked` is served rather than derived by the caller so a
// surface can state the constraint up front instead of only discovering it on a
// refused write (AAD-AC-N-3).
type BaseCurrencyState struct {
	BaseCurrency string
	Locked       bool
	FrozenDeals  int
}

// SetBaseCurrency adopts a new workspace base currency, while that is still
// honest to do.
//
// Every closed deal freezes `fx_rate_to_base` against the CURRENT base
// ([[data-model#DM-FX-4]]), so changing the base afterwards would silently
// reinterpret what those deals were worth — a financial claim this product has
// no authority to restate, and the reason the column's own migration comment
// calls it immutable after the first deal. Before that point it is freely
// changeable, which is the case this exists for: an installation that put the
// wrong currency in its configuration file on day one.
func (s *Store) SetBaseCurrency(ctx context.Context, code string) (BaseCurrencyState, error) {
	var out BaseCurrencyState
	// The same grant that governs the rate sheet: one authority over the
	// currency substrate rather than two that can disagree.
	if err := auth.Require(ctx, "fx_rate", principal.ActionUpdate); err != nil {
		return out, err
	}
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if !isISO4217(normalized) {
		return out, &values.ParseError{
			Field: baseCurrencyColumn, Code: "base_currency_malformed",
			Message: "a base currency is a 3-letter ISO-4217 code",
		}
	}

	err := s.tx(ctx, func(tx pgx.Tx) error {
		frozen, err := frozenRateCount(ctx, tx)
		if err != nil {
			return err
		}
		if frozen > 0 {
			// The count travels with the refusal so the message can say why
			// rather than only saying no.
			out.FrozenDeals = frozen
			return fmt.Errorf("%d closed deal(s) already froze a rate against the current base: %w",
				frozen, apperrors.ErrBaseCurrencyLocked)
		}
		var before string
		if err = tx.QueryRow(ctx,
			`SELECT base_currency FROM workspace WHERE id = $1`,
			storekit.MustWorkspace(ctx)).Scan(&before); err != nil {
			return fmt.Errorf("read current base currency: %w", err)
		}
		if before == normalized {
			out.BaseCurrency = before
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE workspace SET base_currency = $2 WHERE id = $1`,
			storekit.MustWorkspace(ctx), normalized); err != nil {
			return fmt.Errorf("write base currency: %w", err)
		}
		// Audit-only by ratification (EVT-NOEVT-3): the closed event catalog
		// defines no workspace-config verb, and the base currency is config on
		// the same surface as the rate sheet, which is ruled the same way.
		if _, err := storekit.Audit(ctx, tx, "update", "workspace",
			storekit.MustWorkspace(ctx),
			map[string]any{baseCurrencyColumn: before},
			map[string]any{baseCurrencyColumn: normalized}); err != nil {
			return fmt.Errorf("audit base currency change: %w", err)
		}
		out.BaseCurrency = normalized
		return nil
	})
	return out, err
}

// BaseCurrencyStatus answers the current base and whether it is still movable.
func (s *Store) BaseCurrencyStatus(ctx context.Context) (BaseCurrencyState, error) {
	var out BaseCurrencyState
	if err := auth.Require(ctx, "fx_rate", principal.ActionRead); err != nil {
		return out, err
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT base_currency FROM workspace WHERE id = $1`,
			storekit.MustWorkspace(ctx)).Scan(&out.BaseCurrency); err != nil {
			return fmt.Errorf("read base currency: %w", err)
		}
		frozen, err := frozenRateCount(ctx, tx)
		if err != nil {
			return err
		}
		out.FrozenDeals = frozen
		out.Locked = frozen > 0
		return nil
	})
	return out, err
}

// frozenRateCount counts the deals whose worth is already expressed against the
// current base. One is enough to lock it; the count exists for the message.
func frozenRateCount(ctx context.Context, tx pgx.Tx) (int, error) {
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM deal WHERE fx_rate_to_base IS NOT NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count frozen conversion rates: %w", err)
	}
	return n, nil
}
