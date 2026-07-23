// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
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
	// RBAC + pure shape validation BEFORE acquiring a connection, so a denied
	// (403) or malformed-shape (422) write never depends on pool health or
	// consumes a transaction. The clock-dependent effective-day guard lives in
	// the tx (writeFxRate), sampled at write time.
	from, err := s.prepareFxRate(ctx, in)
	if err != nil {
		return FxRateRow{}, err
	}
	var out FxRateRow
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var e error
		out, e = s.writeFxRate(ctx, tx, from, in)
		return e
	})
	if err != nil {
		return FxRateRow{}, err
	}
	return out, nil
}

// SetFxRateInTx applies one effective-dated FX rate through a caller-owned
// transaction — the approval-effect path, where redeem-and-write must commit
// together (a failed write leaves the approval unconsumed and retryable).
// SetFxRate is the standalone-transaction wrapper.
func (s *Store) SetFxRateInTx(ctx context.Context, tx pgx.Tx, in SetFxRateInput) (FxRateRow, error) {
	from, err := s.prepareFxRate(ctx, in)
	if err != nil {
		return FxRateRow{}, err
	}
	return s.writeFxRate(ctx, tx, from, in)
}

// prepareFxRate runs the connection-free, clock-free gates — RBAC admission and
// currency/rate shape — returning the upper-cased from-currency. The effective-
// day resolution and past-date guard are deferred to writeFxRate so they sample
// the clock at write time.
func (s *Store) prepareFxRate(ctx context.Context, in SetFxRateInput) (from string, err error) {
	if err := auth.Require(ctx, "fx_rate", principal.ActionCreate); err != nil {
		return "", err
	}
	return normalizeFxCurrencyRate(in)
}

// writeFxRate does the transactional body: resolve and guard the effective day
// against a clock sampled INSIDE the tx (so a write that waited for the pool
// across UTC midnight is stored against the day it commits — append-forward
// stays true at write time), resolve the workspace base, reject from == base,
// upsert the append-forward row, and audit — all in the caller-owned tx.
func (s *Store) writeFxRate(ctx context.Context, tx pgx.Tx, from string, in SetFxRateInput) (FxRateRow, error) {
	today := s.todayUTC()
	eff := in.EffectiveDate
	if eff.IsZero() {
		eff = today
	}
	// Persist the same UTC-truncated day the past-date guard checks, so a
	// sub-day offset can never store a calendar date different from the validated one.
	effDate := eff.UTC().Truncate(24 * time.Hour)
	if effDate.Before(today) {
		return FxRateRow{}, fxInvalid("effective_date", "fx_rate_past", "effective_date cannot be in the past")
	}
	rate := in.Rate
	var base string
	if err := tx.QueryRow(ctx, `SELECT base_currency FROM workspace WHERE id = $1`,
		storekit.MustWorkspace(ctx)).Scan(&base); err != nil {
		return FxRateRow{}, fmt.Errorf("resolve base currency: %w", err)
	}
	if from == base {
		return FxRateRow{}, fxInvalid("from_currency", "fx_rate_base_self",
			"from_currency equals the base currency (the rate is always 1)")
	}
	var (
		out  FxRateRow
		fxID ids.UUID
	)
	if err := tx.QueryRow(ctx, `
		INSERT INTO fx_rate (workspace_id, from_currency, to_currency, rate, rate_date)
		VALUES ($1, $2, $3, $4::numeric, $5)
		ON CONFLICT (workspace_id, from_currency, to_currency, rate_date)
		DO UPDATE SET rate = EXCLUDED.rate
		RETURNING id, from_currency, to_currency, rate::text, rate_date`,
		storekit.MustWorkspace(ctx), from, base, rate, effDate,
	).Scan(&fxID, &out.FromCurrency, &out.ToCurrency, &out.Rate, &out.RateDate); err != nil {
		return FxRateRow{}, fmt.Errorf("upsert fx_rate: %w", err)
	}
	// Audit-only by ratification (EVT-NOEVT-3): the closed event catalog
	// defines no fx_rate.* type and the rate sheet is workspace config
	// recomputed price-on-read — the same ruling as the deals-owned
	// product rate-card (CreateProduct is audit-only). Ratified in
	// writeshape_test.go; inventing an fx_rate.* verb on the deal stream
	// would violate the closed catalog (contract-first, P3).
	if _, err := storekit.Audit(ctx, tx, "create", "fx_rate", fxID, nil,
		map[string]any{"from": from, "to": base, "rate": rate, "date": effDate}); err != nil {
		return FxRateRow{}, fmt.Errorf("audit fx_rate create: %w", err)
	}
	return out, nil
}

// normalizeFxCurrencyRate validates and upper-cases the currency and checks the
// rate is a positive plain decimal — the connection-free, clock-free shape gates
// (no DB, no time). The effective-day resolution and past-date guard live in
// writeFxRate so they sample the clock at write time, and the from == base check
// needs the base currency and lives in the tx.
func normalizeFxCurrencyRate(in SetFxRateInput) (from string, err error) {
	from = strings.ToUpper(strings.TrimSpace(in.FromCurrency))
	if !isISO4217(from) {
		return "", fxInvalid("from_currency", "fx_rate_currency", "from_currency must be a 3-letter ISO code")
	}
	rate := strings.TrimSpace(in.Rate)
	// rate != in.Rate rejects surrounding whitespace the anchored contract
	// pattern also rejects, so the server's accepted domain matches it exactly.
	if rate != in.Rate || !values.PlainDecimal(rate, 10, 10) {
		return "", fxInvalid("rate", "fx_rate_positive",
			"rate must be a plain decimal (up to 10 integer and 10 fractional digits)")
	}
	if r, _ := new(big.Rat).SetString(rate); r.Sign() <= 0 {
		return "", fxInvalid("rate", "fx_rate_positive", "rate must be greater than zero")
	}
	return from, nil
}

// ListLatestFxRates returns the head of the price sheet — the latest-dated
// row per foreign currency, which MAY be a future-scheduled rate. This is
// the editor's "sheet head" view, deliberately distinct from RateFor's
// as-of-day effective rate (effective_date <= day). Admin/ops read gate.
func (s *Store) ListLatestFxRates(ctx context.Context) ([]FxRateRow, error) {
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

// ListEffectiveFxRates returns the rate in force TODAY per foreign currency —
// the latest row with rate_date <= today (store clock), the list form of
// freezeFx's as-of resolution. Deliberately distinct from ListLatestFxRates
// (sheet head, which may be future-scheduled): a refresh diff and an apply
// precondition compare against what is in force, not what is scheduled.
// Admin/ops read gate.
func (s *Store) ListEffectiveFxRates(ctx context.Context) ([]FxRateRow, error) {
	if err := auth.Require(ctx, "fx_rate", principal.ActionRead); err != nil {
		return nil, err
	}
	today := s.todayUTC()
	var rows []FxRateRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `
			SELECT DISTINCT ON (from_currency) from_currency, to_currency, rate::text, rate_date
			FROM fx_rate WHERE rate_date <= $1
			ORDER BY from_currency, rate_date DESC`, today)
		if err != nil {
			return fmt.Errorf("list effective fx_rate: %w", err)
		}
		defer r.Close()
		rows, err = scanFxRows(r)
		return err
	})
	return rows, err
}

// EffectiveFxRateInTx resolves the rate in force TODAY (store clock) for one
// currency through a caller-owned transaction — the approval-effect
// precondition read, which must see the same state the apply writes into.
// found=false means no rate is in force, a materially different answer from
// any value. Admin/ops read gate.
func (s *Store) EffectiveFxRateInTx(ctx context.Context, tx pgx.Tx, fromCurrency string) (string, bool, error) {
	if err := auth.Require(ctx, "fx_rate", principal.ActionRead); err != nil {
		return "", false, err
	}
	from := strings.ToUpper(strings.TrimSpace(fromCurrency))
	var rate string
	err := tx.QueryRow(ctx, `
		SELECT rate::text FROM fx_rate
		WHERE from_currency = $1 AND rate_date <= $2
		ORDER BY rate_date DESC LIMIT 1`, from, s.todayUTC()).Scan(&rate)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("effective fx_rate for %s: %w", from, err)
	}
	return rate, true, nil
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
