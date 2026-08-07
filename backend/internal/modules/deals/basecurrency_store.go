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
	// FrozenRecords counts the deals and sent offers already expressed against
	// this base. It is what makes Locked explainable — "3 records hold a rate
	// against EUR" rather than a bare no.
	FrozenRecords int
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
		// FOR UPDATE before the count, not after: a freeze samples the base with
		// a shared lock on this same row (freezeFx), so taking it exclusively
		// here is what makes the count and the write one decision. Without it
		// the two transactions never touch a common row, and at READ COMMITTED a
		// deal closing concurrently would freeze against the base this one is
		// about to replace — leaving a rate pinned to a base that no longer
		// exists, which the guard can never afterwards detect.
		var before string
		if err := tx.QueryRow(ctx,
			`SELECT base_currency FROM workspace WHERE id = $1 FOR UPDATE`,
			storekit.MustWorkspace(ctx)).Scan(&before); err != nil {
			return fmt.Errorf("read current base currency: %w", err)
		}
		frozen, err := frozenRateCount(ctx, tx)
		if err != nil {
			return err
		}
		if frozen > 0 {
			// The count is in the message rather than the returned state: the
			// caller discards the state on error, and this is the only place a
			// human learns WHY the base stopped moving.
			return fmt.Errorf("%d record(s) already froze a rate against the current base: %w",
				frozen, apperrors.ErrBaseCurrencyLocked)
		}
		if err := refuseWhenRateSheetIsPriced(ctx, tx, before, normalized); err != nil {
			return err
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
		out.FrozenRecords = frozen
		out.Locked = frozen > 0
		return nil
	})
	return out, err
}

// refuseWhenRateSheetIsPriced stops a base change that would leave the existing
// rate sheet lying.
//
// Every fx_rate row stores the base it converts INTO as `to_currency`, fixed
// when the rate was entered. Moving the base does not rewrite them, so a sheet
// entered against EUR would go on being served beside a base of USD — the read
// side would report USD rates that are in fact EUR ones. Re-targeting them is
// not available either: nobody can restate a EUR→USD rate as a CHF→USD one.
//
// So the base moves only while nothing is priced against it. That is exactly
// the case this write exists for — an installation correcting the currency it
// put in its configuration file on day one, before it entered any rates.
func refuseWhenRateSheetIsPriced(ctx context.Context, tx pgx.Tx, before, wanted string) error {
	if before == wanted {
		return nil
	}
	var priced int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM fx_rate WHERE to_currency = $1`, before).Scan(&priced); err != nil {
		return fmt.Errorf("count rates priced against the current base: %w", err)
	}
	if priced == 0 {
		return nil
	}
	return fmt.Errorf(
		"%d rate(s) convert into %s and cannot be restated as %s rates; clear the rate sheet first: %w",
		priced, before, wanted, apperrors.ErrBaseCurrencyLocked)
}

// frozenRateTables are every table holding an `fx_rate_to_base` — the column
// that expresses one record's worth against the base in force when it froze.
// A deal freezes on close; an offer freezes on send, and its figure has already
// left the building in a PDF. Counting only deals would let a workspace that has
// sent offers and closed nothing flip the base and restate all of them.
//
// TestTheBaseCurrencyGuardCountsEveryFrozenRate derives this list from the
// schema, so a future table carrying the column fails that test rather than
// quietly widening the hole.
var frozenRateTables = []string{"deal", "offer"}

// frozenRateCount counts the records whose worth is already expressed against
// the current base. One is enough to lock it; the count exists for the message.
func frozenRateCount(ctx context.Context, tx pgx.Tx) (int, error) {
	total := 0
	for _, table := range frozenRateTables {
		var n int
		// The table name comes from the package-level list above, never from a
		// caller, so the interpolation carries nothing a request chose.
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM `+table+` WHERE fx_rate_to_base IS NOT NULL`).Scan(&n); err != nil {
			return 0, fmt.Errorf("count frozen conversion rates in %s: %w", table, err)
		}
		total += n
	}
	return total, nil
}
