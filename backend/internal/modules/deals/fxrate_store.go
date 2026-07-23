// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// FxRateRow is one effective-dated FX rate: the rate that converts
// FromCurrency into the workspace base (ToCurrency) as of RateDate. Rate
// is carried as a decimal string (numeric(20,10)) — never a float.
type FxRateRow struct {
	FromCurrency string
	ToCurrency   string
	Rate         string
	RateDate     time.Time
}

// SetFxRateInput sets one effective-dated rate. EffectiveDate is the UTC
// day the rate takes effect; it may be today or later, never the past
// (strict append-forward — a past-dated row prices historical rollups and
// must never change).
type SetFxRateInput struct {
	FromCurrency  string
	Rate          string
	EffectiveDate time.Time
}

// FxRateValidationError is this module's typed 422 for a rejected rate
// write; writeStoreErr maps it to httperr.Validation on the wire.
type FxRateValidationError struct {
	Field   string
	Code    string
	Message string
}

func (e *FxRateValidationError) Error() string { return e.Message }

func fxInvalid(field, code, message string) error {
	return &FxRateValidationError{Field: field, Code: code, Message: message}
}

// isISO4217 answers whether s is a 3-letter uppercase ISO-4217-shaped code.
func isISO4217(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func (s *Store) todayUTC() time.Time {
	return s.clock().UTC().Truncate(24 * time.Hour)
}

// SetFxRate appends (or corrects, same UTC day) one effective-dated FX
// rate. Admin/ops-gated; append-forward (rejects a past effective date);
// resolves ToCurrency to the workspace base and rejects from == base.
func (s *Store) SetFxRate(ctx context.Context, in SetFxRateInput) (FxRateRow, error) {
	if err := auth.Require(ctx, "fx_rate", principal.ActionCreate); err != nil {
		return FxRateRow{}, err
	}
	from, err := normalizeFxInput(in, s.todayUTC())
	if err != nil {
		return FxRateRow{}, err
	}

	var out FxRateRow
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var base string
		if err := tx.QueryRow(ctx, `SELECT base_currency FROM workspace WHERE id = $1`,
			storekit.MustWorkspace(ctx)).Scan(&base); err != nil {
			return fmt.Errorf("resolve base currency: %w", err)
		}
		if from == base {
			return fxInvalid("from_currency", "fx_rate_base_self",
				"from_currency equals the base currency (the rate is always 1)")
		}
		var fxID ids.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO fx_rate (workspace_id, from_currency, to_currency, rate, rate_date)
			VALUES ($1, $2, $3, $4::numeric, $5)
			ON CONFLICT (workspace_id, from_currency, to_currency, rate_date)
			DO UPDATE SET rate = EXCLUDED.rate
			RETURNING id, from_currency, to_currency, rate::text, rate_date`,
			storekit.MustWorkspace(ctx), from, base, in.Rate, in.EffectiveDate,
		).Scan(&fxID, &out.FromCurrency, &out.ToCurrency, &out.Rate, &out.RateDate); err != nil {
			return fmt.Errorf("upsert fx_rate: %w", err)
		}
		auditID, err := storekit.Audit(ctx, tx, "create", "fx_rate", fxID, nil,
			map[string]any{"from": from, "to": base, "rate": in.Rate, "date": in.EffectiveDate})
		if err != nil {
			return fmt.Errorf("audit fx_rate set: %w", err)
		}
		return storekit.Emit(ctx, tx, auditID, "fx_rate.appended", "fx_rate", fxID,
			map[string]any{"from": from, "to": base, "rate": in.Rate})
	})
	if err != nil {
		return FxRateRow{}, err
	}
	return out, nil
}

// normalizeFxInput validates and upper-cases the currency, checks the rate
// is a positive decimal, and rejects a past effective date. It does not
// touch the DB (the from == base check needs the base currency and lives in
// the tx), so it is unit-testable in isolation.
func normalizeFxInput(in SetFxRateInput, today time.Time) (from string, err error) {
	from = strings.ToUpper(strings.TrimSpace(in.FromCurrency))
	if !isISO4217(from) {
		return "", fxInvalid("from_currency", "fx_rate_currency", "from_currency must be a 3-letter ISO code")
	}
	if r, ok := new(big.Rat).SetString(strings.TrimSpace(in.Rate)); !ok || r.Sign() <= 0 {
		return "", fxInvalid("rate", "fx_rate_positive", "rate must be a positive decimal")
	}
	if in.EffectiveDate.UTC().Truncate(24 * time.Hour).Before(today) {
		return "", fxInvalid("effective_date", "fx_rate_past", "effective_date cannot be in the past")
	}
	return from, nil
}

// ListEffectiveFxRates returns the current (latest effective) rate per
// foreign currency. Admin/ops read gate.
func (s *Store) ListEffectiveFxRates(ctx context.Context) ([]FxRateRow, error) {
	if err := auth.Require(ctx, "fx_rate", principal.ActionRead); err != nil {
		return nil, err
	}
	var rows []FxRateRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `
			SELECT DISTINCT ON (from_currency) from_currency, to_currency, rate::text, rate_date
			FROM fx_rate
			ORDER BY from_currency, rate_date DESC`)
		if err != nil {
			return fmt.Errorf("list fx_rate: %w", err)
		}
		defer r.Close()
		rows, err = scanFxRows(r)
		return err
	})
	return rows, err
}

// FxRateHistory returns every effective-dated row for one pair, newest
// first (read-only history). Admin/ops read gate.
func (s *Store) FxRateHistory(ctx context.Context, fromCurrency string) ([]FxRateRow, error) {
	if err := auth.Require(ctx, "fx_rate", principal.ActionRead); err != nil {
		return nil, err
	}
	from := strings.ToUpper(strings.TrimSpace(fromCurrency))
	var rows []FxRateRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `
			SELECT from_currency, to_currency, rate::text, rate_date
			FROM fx_rate WHERE from_currency = $1
			ORDER BY rate_date DESC`, from)
		if err != nil {
			return fmt.Errorf("fx_rate history: %w", err)
		}
		defer r.Close()
		rows, err = scanFxRows(r)
		return err
	})
	return rows, err
}

func scanFxRows(r pgx.Rows) ([]FxRateRow, error) {
	var out []FxRateRow
	for r.Next() {
		var row FxRateRow
		if err := r.Scan(&row.FromCurrency, &row.ToCurrency, &row.Rate, &row.RateDate); err != nil {
			return nil, fmt.Errorf("scan fx_rate: %w", err)
		}
		out = append(out, row)
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("iterate fx_rate: %w", err)
	}
	return out, nil
}
